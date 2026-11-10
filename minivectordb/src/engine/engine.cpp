#include "engine/engine.h"

#include <cctype>
#include <cstdio>
#include <exception>
#include <stdexcept>
#include <string_view>
#include <utility>

#ifdef MVDB_TIMING
#include <chrono>
#endif

#include "common/error.h"
#include "exec/apply.h"
#include "sql/parser.h"
#include "storage/persistence.h"

namespace mvdb {
namespace {

#ifdef MVDB_TIMING
struct Timers {
  double parse = 0;
  double execute = 0;
  double wal = 0;
  double apply = 0;
  ~Timers() {
    std::fprintf(stderr,
                 "timing: parse=%.3f exec=%.3f wal=%.3f apply=%.3f total=%.3f\n",
                 parse, execute, wal, apply, parse + execute + wal + apply);
  }
};
Timers g_timers;

double seconds_since(std::chrono::steady_clock::time_point start) {
  return std::chrono::duration<double>(std::chrono::steady_clock::now() - start)
      .count();
}
#endif

StatementResult error_result(std::string message, std::string code) {
  StatementResult result;
  result.status = Status::kError;
  result.message = std::move(message);
  result.error_code = std::move(code);
  return result;
}

bool is_space_char(char c) {
  return std::isspace(static_cast<unsigned char>(c)) != 0;
}

// 协议层一条请求就是一条语句，允许末尾带分号与首尾空白。
std::string_view trim_statement(std::string_view text) {
  while (!text.empty() && is_space_char(text.front())) text.remove_prefix(1);
  while (!text.empty() && is_space_char(text.back())) text.remove_suffix(1);
  if (!text.empty() && text.back() == ';') {
    text.remove_suffix(1);
    while (!text.empty() && is_space_char(text.back())) text.remove_suffix(1);
  }
  return text;
}

std::string classify_error(std::string_view message) {
  if (message.rfind("duplicate primary key", 0) == 0) return "duplicate_key";
  if (message.rfind("unsupported statement", 0) == 0) return "unsupported_sql";
  if (message.rfind("unknown table", 0) == 0) return "unknown_table";
  if (message.rfind("unknown column", 0) == 0) return "unknown_column";
  if (message.rfind("type mismatch", 0) == 0 ||
      message.rfind("value too long", 0) == 0 ||
      message.rfind("vector dimension", 0) == 0 ||
      message.rfind("invalid vector", 0) == 0 ||
      message.rfind("vector value", 0) == 0) {
    return "invalid_value";
  }
  return "error";
}

}  // namespace

Engine::Engine(std::filesystem::path data_dir, std::size_t buffer_pages)
    : data_dir_(std::move(data_dir)),
      pool_(&files_, buffer_pages),
      catalog_(&files_, &pool_, data_dir_) {
  std::string error;
  if (!storage::load_catalog(data_dir_, &catalog_, &error)) {
    throw std::runtime_error("failed to load data: " + error);
  }
  if (!wal_.open(data_dir_ / "wal.log", &error)) {
    throw std::runtime_error("failed to open WAL: " + error);
  }
  bool recovered = false;
  if (!wal_.replay(&catalog_, &recovered, &error)) {
    throw std::runtime_error("failed to replay WAL: " + error);
  }
  // 恢复完成后立即检查点：回写重放产生的脏页并截断 WAL。
  if (recovered && !checkpoint(&error)) {
    throw std::runtime_error("failed to checkpoint after recovery: " + error);
  }
}

Engine::~Engine() { shutdown(); }

bool Engine::checkpoint(std::string* error) {
  try {
    pool_.flush_all();
    if (!storage::save_catalog(data_dir_, catalog_, error)) return false;
    if (!wal_.truncate(error)) return false;
  } catch (const SqlError& exception) {
    *error = exception.what();
    return false;
  }
  dirty_ = false;
  return true;
}

void Engine::shutdown() {
  if (shut_down_) return;
  shut_down_ = true;
  if (in_transaction_) rollback_transaction();
  if (dirty_) {
    std::string error;
    if (!checkpoint(&error)) {
      std::fprintf(stderr, "minivectordb: checkpoint failed: %s\n",
                   error.c_str());
    }
  }
  wal_.close();
  files_.close_all();
}

void Engine::track_ddl(const storage::WalRecord& record) {
  if (const auto* create = std::get_if<storage::CreateTableRecord>(&record)) {
    created_tables_.push_back(create->schema.name);
    return;
  }
  if (const auto* index = std::get_if<storage::CreateIndexRecord>(&record)) {
    storage::HeapTable* table = catalog_.find(index->table);
    std::string previous =
        table != nullptr ? table->schema().vector_index_name : std::string();
    index_backup_.emplace_back(lowercase(index->table), std::move(previous));
  }
}

void Engine::update_overlay(const storage::WalRecord& record) {
  if (const auto* insert = std::get_if<storage::InsertRecord>(&record)) {
    storage::HeapTable* table = catalog_.find(insert->table);
    if (table == nullptr) return;
    exec::TableOverlay& delta = overlay_[lowercase(insert->table)];
    for (const std::vector<Value>& values : insert->rows) {
      auto const key = std::get<std::int64_t>(
          values[static_cast<std::size_t>(table->schema().pk_index)]);
      delta.inserts[key] = values;
      delta.deletes.erase(key);
      delta.updates.erase(key);
    }
    return;
  }
  if (const auto* update = std::get_if<storage::UpdateRecord>(&record)) {
    exec::TableOverlay& delta = overlay_[lowercase(update->table)];
    auto const inserted = delta.inserts.find(update->pk);
    if (inserted != delta.inserts.end()) {
      for (const auto& [column, value] : update->values) {
        inserted->second[static_cast<std::size_t>(column)] = value;
      }
      return;
    }
    if (delta.deletes.count(update->pk) != 0) return;
    auto& columns = delta.updates[update->pk];
    for (const auto& [column, value] : update->values) {
      bool replaced = false;
      for (auto& entry : columns) {
        if (entry.first == column) {
          entry.second = value;
          replaced = true;
          break;
        }
      }
      if (!replaced) columns.emplace_back(column, value);
    }
    return;
  }
  const auto& remove = std::get<storage::DeleteRecord>(record);
  exec::TableOverlay& delta = overlay_[lowercase(remove.table)];
  if (delta.inserts.erase(remove.pk) == 0) {
    delta.deletes.insert(remove.pk);
    delta.updates.erase(remove.pk);
  }
}

void Engine::rollback_transaction() {
  for (auto it = created_tables_.rbegin(); it != created_tables_.rend(); ++it) {
    catalog_.erase(*it);
  }
  for (auto it = index_backup_.rbegin(); it != index_backup_.rend(); ++it) {
    storage::HeapTable* table = catalog_.find(it->first);
    if (table != nullptr) {
      table->mutable_schema().vector_index_name = it->second;
    }
  }
  created_tables_.clear();
  index_backup_.clear();
  pending_.clear();
  overlay_.clear();
  in_transaction_ = false;
}

StatementResult Engine::execute(std::string_view sql_text) {
  ++executed_;
  try {
#ifdef MVDB_TIMING
    auto const t_start = std::chrono::steady_clock::now();
#endif
    sql::Statement statement = sql::parse(trim_statement(sql_text));
#ifdef MVDB_TIMING
    auto const t_parsed = std::chrono::steady_clock::now();
    g_timers.parse += seconds_since(t_start);
#endif
    if (auto* transaction = std::get_if<sql::TransactionStmt>(&statement)) {
      return execute_transaction(*transaction);
    }

    std::vector<storage::WalRecord> changes;
    StatementResult result =
        exec::execute(statement, catalog_,
                      in_transaction_ ? &overlay_ : nullptr, &changes);
#ifdef MVDB_TIMING
    auto const t_executed = std::chrono::steady_clock::now();
    g_timers.execute += seconds_since(t_parsed);
#endif
    if (changes.empty()) return result;

    if (in_transaction_) {
      for (storage::WalRecord& record : changes) {
        if (storage::is_ddl(record)) {
          track_ddl(record);
          apply_record(record, &catalog_);  // DDL 立即生效，回滚时撤销
        } else {
          update_overlay(record);  // DML 只进入覆盖层
        }
        pending_.push_back(std::move(record));
      }
    } else {
      std::string error;
      if (!wal_.append_commit(changes, catalog_, &error)) {
        return error_result("failed to persist: " + error, "storage_error");
      }
#ifdef MVDB_TIMING
      auto const t_wal = std::chrono::steady_clock::now();
      g_timers.wal += seconds_since(t_executed);
#endif
      for (const storage::WalRecord& record : changes) {
        apply_record(record, &catalog_);
      }
#ifdef MVDB_TIMING
      g_timers.apply += seconds_since(t_wal);
#endif
      dirty_ = true;
    }
    return result;
  } catch (const SqlError& exception) {
    return error_result(exception.what(), classify_error(exception.what()));
  } catch (const std::exception& exception) {
    return error_result(std::string("internal error: ") + exception.what(),
                        "internal_error");
  }
}

StatementResult Engine::execute_transaction(
    const sql::TransactionStmt& stmt) {
  switch (stmt.kind) {
    case sql::TransactionStmt::Kind::kBegin:
      if (in_transaction_) {
        return error_result("transaction already in progress",
                            "transaction_conflict");
      }
      overlay_.clear();
      pending_.clear();
      created_tables_.clear();
      index_backup_.clear();
      in_transaction_ = true;
      return StatementResult{};

    case sql::TransactionStmt::Kind::kCommit: {
      if (!in_transaction_) return StatementResult{};
      if (!pending_.empty()) {
        std::string error;
        if (!wal_.append_commit(pending_, catalog_, &error)) {
          return error_result("failed to persist: " + error, "storage_error");
        }
        for (const storage::WalRecord& record : pending_) {
          apply_record(record, &catalog_);
        }
        dirty_ = true;
      }
      overlay_.clear();
      pending_.clear();
      created_tables_.clear();
      index_backup_.clear();
      in_transaction_ = false;
      return StatementResult{};
    }

    case sql::TransactionStmt::Kind::kRollback:
      if (in_transaction_) rollback_transaction();
      return StatementResult{};
  }
  return error_result("internal error: unknown transaction command",
                      "internal_error");
}

}  // namespace mvdb
