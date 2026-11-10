#pragma once

#include <cstddef>
#include <cstdint>
#include <filesystem>
#include <string>
#include <string_view>
#include <utility>
#include <vector>

#include "common/result.h"
#include "exec/catalog.h"
#include "exec/executor.h"
#include "storage/buffer_pool.h"
#include "storage/file_manager.h"
#include "storage/wal.h"

namespace mvdb {

// 数据库入口：解析、执行、事务与持久化管理。
class Engine {
 public:
  Engine(std::filesystem::path data_dir, std::size_t buffer_pages);
  ~Engine();

  Engine(const Engine&) = delete;
  Engine& operator=(const Engine&) = delete;

  StatementResult execute(std::string_view sql);

  // 回滚未提交事务、回写脏页并把表定义落盘。可重复调用。
  void shutdown();

  const std::filesystem::path& data_dir() const { return data_dir_; }
  std::uint64_t executed_statements() const { return executed_; }

 private:
  StatementResult execute_transaction(const sql::TransactionStmt& stmt);
  void update_overlay(const storage::WalRecord& record);
  void track_ddl(const storage::WalRecord& record);
  void rollback_transaction();
  bool checkpoint(std::string* error);

  std::filesystem::path data_dir_;
  storage::FileManager files_;
  storage::BufferPool pool_;
  Catalog catalog_;
  storage::WalFile wal_;
  std::vector<storage::WalRecord> pending_;
  exec::OverlayMap overlay_;
  std::vector<std::string> created_tables_;
  std::vector<std::pair<std::string, std::string>> index_backup_;
  bool in_transaction_ = false;
  bool dirty_ = false;
  bool shut_down_ = false;
  std::uint64_t executed_ = 0;
};

}  // namespace mvdb
