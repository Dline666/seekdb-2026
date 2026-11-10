#include "exec/executor.h"

#include <algorithm>
#include <cmath>
#include <cstddef>
#include <cstdint>
#include <numeric>
#include <string>
#include <string_view>
#include <unordered_map>
#include <unordered_set>
#include <utility>
#include <vector>

#include "common/error.h"
#include "common/schema.h"
#include "storage/record.h"

namespace mvdb::exec {
namespace {

using sql::CompareOp;
using sql::Literal;
using storage::HeapTable;
using storage::RecordView;
using storage::Rid;

HeapTable& require_table(Catalog& catalog, const std::string& name) {
  HeapTable* table = catalog.find(name);
  if (table == nullptr) throw SqlError("unknown table '" + name + "'");
  return *table;
}

const TableOverlay* find_delta(const OverlayMap* overlay,
                               const std::string& table_name) {
  if (overlay == nullptr) return nullptr;
  auto const it = overlay->find(lowercase(table_name));
  return it == overlay->end() ? nullptr : &it->second;
}

int resolve_column(const Schema& schema, const std::string& name,
                   const char* context) {
  int const index = schema.find_column(name);
  if (index < 0) {
    throw SqlError("unknown column '" + name + "' in " + context);
  }
  return index;
}

Value value_from_literal(const Literal& literal, const ColumnType& type,
                         const std::string& column_name) {
  switch (type.kind) {
    case TypeKind::kBigInt:
      if (literal.kind != Literal::Kind::kInt) {
        throw SqlError("type mismatch for column '" + column_name +
                       "': expected BIGINT");
      }
      return literal.int_value;
    case TypeKind::kVarchar:
      if (literal.kind != Literal::Kind::kString) {
        throw SqlError("type mismatch for column '" + column_name +
                       "': expected VARCHAR");
      }
      if (utf8_length(literal.text) > type.size) {
        throw SqlError("value too long for column '" + column_name + "' (max " +
                       std::to_string(type.size) + " characters)");
      }
      return literal.text;
    case TypeKind::kVector: {
      if (literal.kind == Literal::Kind::kVector) {
        return Vector{to_vector_f32(literal.vector, type.size)};
      }
      if (literal.kind == Literal::Kind::kString) {
        return Vector{parse_vector_f32(literal.text, type.size)};
      }
      throw SqlError("type mismatch for column '" + column_name +
                     "': expected VECTOR");
    }
  }
  throw SqlError("internal error: unknown column type");
}

std::vector<float> query_vector(const Literal& literal, std::uint32_t dim) {
  if (literal.kind == Literal::Kind::kVector) {
    return to_vector_f32(literal.vector, dim);
  }
  if (literal.kind == Literal::Kind::kString) {
    return parse_vector_f32(literal.text, dim);
  }
  throw SqlError("L2_DISTANCE requires a vector literal");
}

bool pk_visible(const HeapTable& table, const TableOverlay* overlay,
                std::int64_t pk) {
  if (overlay != nullptr) {
    if (overlay->inserts.count(pk) != 0) return true;
    if (overlay->deletes.count(pk) != 0) return false;
  }
  Rid rid;
  return table.find_rid(pk, &rid);
}

// ---------- 统一行视图 ----------

class RowRef {
 public:
  RowRef(const RecordView& view, const Schema& schema)
      : view_(&view), values_(nullptr), schema_(&schema) {}
  RowRef(const std::vector<Value>& values, const Schema& schema)
      : view_(nullptr), values_(&values), schema_(&schema) {}

  std::int64_t pk() const {
    return bigint(static_cast<std::size_t>(schema_->pk_index));
  }

  std::int64_t bigint(std::size_t column) const {
    if (view_ != nullptr) return view_->bigint(column);
    return std::get<std::int64_t>((*values_)[column]);
  }

  std::string_view varchar(std::size_t column) const {
    if (view_ != nullptr) return view_->varchar(column);
    return std::get<std::string>((*values_)[column]);
  }

  Value value(std::size_t column) const {
    if (view_ != nullptr) return view_->value(column);
    return (*values_)[column];
  }

  double distance(std::size_t column, const std::vector<float>& query) const {
    if (view_ != nullptr) {
      return storage::l2_distance_bytes(
          view_->vector_bytes(column),
          static_cast<std::uint32_t>(query.size()), query);
    }
    return storage::l2_distance_vectors(
        std::get<Vector>((*values_)[column]).data, query);
  }

 private:
  const RecordView* view_;
  const std::vector<Value>* values_;
  const Schema* schema_;
};

// ---------- WHERE ----------

struct BoundTerm {
  int column = 0;
  TypeKind kind = TypeKind::kBigInt;
  CompareOp op = CompareOp::kEq;
  Value literal;
  bool column_on_left = true;
};

std::vector<BoundTerm> bind_condition(const sql::Condition& condition,
                                      const Schema& schema) {
  std::vector<BoundTerm> terms;
  terms.reserve(condition.terms.size());
  for (const sql::Condition::Term& term : condition.terms) {
    int const index = resolve_column(schema, term.column, "WHERE clause");
    const Column& column = schema.columns[static_cast<std::size_t>(index)];
    if (column.type.kind == TypeKind::kVector) {
      throw SqlError("vector comparison is not supported for column '" +
                     column.name + "'");
    }
    BoundTerm bound;
    bound.column = index;
    bound.kind = column.type.kind;
    bound.op = term.op;
    bound.literal = value_from_literal(term.literal, column.type, column.name);
    bound.column_on_left = term.column_on_left;
    terms.push_back(std::move(bound));
  }
  return terms;
}

CompareOp mirror(CompareOp op) {
  switch (op) {
    case CompareOp::kEq:
      return CompareOp::kEq;
    case CompareOp::kLt:
      return CompareOp::kGt;
    case CompareOp::kLe:
      return CompareOp::kGe;
    case CompareOp::kGt:
      return CompareOp::kLt;
    case CompareOp::kGe:
      return CompareOp::kLe;
  }
  return CompareOp::kEq;
}

bool apply_comparison(int cmp, CompareOp op) {
  switch (op) {
    case CompareOp::kEq:
      return cmp == 0;
    case CompareOp::kLt:
      return cmp < 0;
    case CompareOp::kLe:
      return cmp <= 0;
    case CompareOp::kGt:
      return cmp > 0;
    case CompareOp::kGe:
      return cmp >= 0;
  }
  return false;
}

bool row_matches(const RowRef& row, const std::vector<BoundTerm>& terms) {
  for (const BoundTerm& term : terms) {
    CompareOp const op = term.column_on_left ? term.op : mirror(term.op);
    if (term.kind == TypeKind::kBigInt) {
      std::int64_t const value = row.bigint(static_cast<std::size_t>(term.column));
      std::int64_t const literal = std::get<std::int64_t>(term.literal);
      int const cmp =
          value < literal ? -1 : (value > literal ? 1 : 0);
      if (!apply_comparison(cmp, op)) return false;
    } else {
      std::string_view const value =
          row.varchar(static_cast<std::size_t>(term.column));
      const std::string& literal = std::get<std::string>(term.literal);
      if (!apply_comparison(value.compare(literal), op)) return false;
    }
  }
  return true;
}

// ---------- 排序 ----------

struct KeyValue {
  enum class Kind { kInt, kDouble, kString } kind = Kind::kInt;
  std::int64_t i = 0;
  double d = 0.0;
  std::string s;
};

int compare_key(const KeyValue& lhs, const KeyValue& rhs) {
  switch (lhs.kind) {
    case KeyValue::Kind::kInt:
      return lhs.i < rhs.i ? -1 : (lhs.i > rhs.i ? 1 : 0);
    case KeyValue::Kind::kDouble:
      return lhs.d < rhs.d ? -1 : (lhs.d > rhs.d ? 1 : 0);
    case KeyValue::Kind::kString:
      return lhs.s.compare(rhs.s);
  }
  return 0;
}

struct SortSpec {
  enum class Kind { kColumn, kDistance } kind = Kind::kColumn;
  int column = 0;
  std::vector<float> query;
  bool descending = false;
};

struct OutputColumn {
  enum class Kind { kColumn, kDistance } kind = Kind::kColumn;
  int column = 0;
  std::vector<float> query;
  std::string name;
};

KeyValue key_for(const RowRef& row, const SortSpec& spec, const Schema& schema) {
  KeyValue key;
  if (spec.kind == SortSpec::Kind::kDistance) {
    key.kind = KeyValue::Kind::kDouble;
    key.d = row.distance(static_cast<std::size_t>(spec.column), spec.query);
    return key;
  }
  const Column& column = schema.columns[static_cast<std::size_t>(spec.column)];
  if (column.type.kind == TypeKind::kBigInt) {
    key.kind = KeyValue::Kind::kInt;
    key.i = row.bigint(static_cast<std::size_t>(spec.column));
    return key;
  }
  key.kind = KeyValue::Kind::kString;
  key.s = std::string(row.varchar(static_cast<std::size_t>(spec.column)));
  return key;
}

struct Candidate {
  std::vector<KeyValue> keys;
  std::int64_t pk = 0;
};

bool worse_than(const Candidate& lhs, const Candidate& rhs,
                const std::vector<SortSpec>& sorts) {
  for (std::size_t k = 0; k < sorts.size(); ++k) {
    int const cmp = compare_key(lhs.keys[k], rhs.keys[k]);
    if (cmp != 0) return sorts[k].descending ? cmp < 0 : cmp > 0;
  }
  return lhs.pk > rhs.pk;  // 并列按主键升序
}

// 直接用栈上算好的 key 与已有候选比较，避免为每个匹配行构造 Candidate。
bool worse_than_keys(const std::vector<KeyValue>& keys, std::int64_t pk,
                     const Candidate& other,
                     const std::vector<SortSpec>& sorts) {
  for (std::size_t k = 0; k < sorts.size(); ++k) {
    int const cmp = compare_key(keys[k], other.keys[k]);
    if (cmp != 0) return sorts[k].descending ? cmp < 0 : cmp > 0;
  }
  return pk > other.pk;
}

class Collector {
 public:
  Collector(const std::vector<SortSpec>* sorts, bool bounded, std::size_t limit)
      : sorts_(sorts), bounded_(bounded), limit_(limit) {}

  // 有排序键时使用；只有真正入选的候选才会复制 keys。
  void consider(const std::vector<KeyValue>& keys, std::int64_t pk) {
    if (bounded_) {
      if (limit_ == 0) return;
      // 以"更好者优先"建堆：堆顶是当前最差的候选。
      auto better = [this](const Candidate& lhs, const Candidate& rhs) {
        return worse_than(rhs, lhs, *sorts_);
      };
      if (items_.size() < limit_) {
        items_.push_back(Candidate{keys, pk});
        std::push_heap(items_.begin(), items_.end(), better);
        return;
      }
      if (worse_than_keys(keys, pk, items_.front(), *sorts_)) return;
      std::pop_heap(items_.begin(), items_.end(), better);
      items_.pop_back();
      items_.push_back(Candidate{keys, pk});
      std::push_heap(items_.begin(), items_.end(), better);
      return;
    }
    items_.push_back(Candidate{keys, pk});
  }

  // 无排序键（按扫描顺序）时使用。
  void consider_unsorted(std::int64_t pk) {
    items_.push_back(Candidate{std::vector<KeyValue>{}, pk});
  }

  std::size_t size() const { return items_.size(); }

  std::vector<Candidate> finish() {
    if (sorts_ != nullptr && !sorts_->empty()) {
      std::sort(items_.begin(), items_.end(),
                [this](const Candidate& lhs, const Candidate& rhs) {
                  return worse_than(rhs, lhs, *sorts_);
                });
    }
    if (bounded_ && items_.size() > limit_) items_.resize(limit_);
    return std::move(items_);
  }

 private:
  const std::vector<SortSpec>* sorts_;
  bool bounded_;
  std::size_t limit_;
  std::vector<Candidate> items_;
};

// ---------- 扫描 ----------

template <typename Fn>
void for_each_row(HeapTable& table, const TableOverlay* overlay, Fn&& fn) {
  const Schema& schema = table.schema();
  table.scan([&](Rid /*rid*/, std::string_view record) {
    RecordView view(record, schema);
    std::int64_t const pk = view.pk();
    if (overlay != nullptr) {
      if (overlay->deletes.count(pk) != 0) return;
      auto const updated = overlay->updates.find(pk);
      if (updated != overlay->updates.end()) {
        std::vector<Value> merged = storage::decode_record(record, schema);
        for (const auto& [column, value] : updated->second) {
          merged[static_cast<std::size_t>(column)] = value;
        }
        RowRef ref(merged, schema);
        fn(ref);
        return;
      }
    }
    RowRef ref(view, schema);
    fn(ref);
  });
  if (overlay != nullptr) {
    for (const auto& entry : overlay->inserts) {
      RowRef ref(entry.second, schema);
      fn(ref);
    }
  }
}

std::vector<Value> load_row(HeapTable& table, const TableOverlay* overlay,
                            std::int64_t pk) {
  if (overlay != nullptr) {
    auto const inserted = overlay->inserts.find(pk);
    if (inserted != overlay->inserts.end()) return inserted->second;
  }
  std::string record;
  if (!table.read(pk, &record)) {
    throw SqlError("internal error: row vanished during projection");
  }
  std::vector<Value> values = storage::decode_record(record, table.schema());
  if (overlay != nullptr) {
    auto const updated = overlay->updates.find(pk);
    if (updated != overlay->updates.end()) {
      for (const auto& [column, value] : updated->second) {
        values[static_cast<std::size_t>(column)] = value;
      }
    }
  }
  return values;
}

// ---------- SELECT ----------

StatementResult execute_select(const sql::SelectStmt& stmt, Catalog& catalog,
                               const OverlayMap* overlay) {
  HeapTable& table = require_table(catalog, stmt.table);
  const Schema& schema = table.schema();
  const TableOverlay* const delta = find_delta(overlay, stmt.table);

  std::vector<OutputColumn> outputs;
  std::unordered_map<std::string, std::size_t> aliases;

  for (const sql::SelectItem& item : stmt.items) {
    if (item.star) {
      for (std::size_t i = 0; i < schema.columns.size(); ++i) {
        OutputColumn out;
        out.kind = OutputColumn::Kind::kColumn;
        out.column = static_cast<int>(i);
        out.name = schema.columns[i].name;
        outputs.push_back(std::move(out));
      }
      continue;
    }
    if (item.is_distance) {
      int const index = resolve_column(schema, item.column, "select list");
      const Column& column = schema.columns[static_cast<std::size_t>(index)];
      if (column.type.kind != TypeKind::kVector) {
        throw SqlError("L2_DISTANCE requires a vector column, '" + column.name +
                       "' is not");
      }
      OutputColumn out;
      out.kind = OutputColumn::Kind::kDistance;
      out.column = index;
      out.query = query_vector(item.query, column.type.size);
      out.name =
          item.alias.empty() ? "L2_DISTANCE(" + column.name + ")" : item.alias;
      if (!item.alias.empty()) {
        aliases.emplace(lowercase(item.alias), outputs.size());
      }
      outputs.push_back(std::move(out));
      continue;
    }
    int const index = resolve_column(schema, item.column, "select list");
    OutputColumn out;
    out.kind = OutputColumn::Kind::kColumn;
    out.column = index;
    out.name = item.alias.empty()
                   ? schema.columns[static_cast<std::size_t>(index)].name
                   : item.alias;
    if (!item.alias.empty()) {
      aliases.emplace(lowercase(item.alias), outputs.size());
    }
    outputs.push_back(std::move(out));
  }

  std::vector<BoundTerm> const terms =
      stmt.has_where ? bind_condition(stmt.where, schema)
                     : std::vector<BoundTerm>{};

  std::vector<SortSpec> sorts;
  for (const sql::OrderItem& item : stmt.order_by) {
    SortSpec spec;
    spec.descending = item.descending;
    if (item.is_distance) {
      int const index = resolve_column(schema, item.column, "ORDER BY");
      const Column& column = schema.columns[static_cast<std::size_t>(index)];
      if (column.type.kind != TypeKind::kVector) {
        throw SqlError("L2_DISTANCE requires a vector column, '" + column.name +
                       "' is not");
      }
      spec.kind = SortSpec::Kind::kDistance;
      spec.column = index;
      spec.query = query_vector(item.query, column.type.size);
      sorts.push_back(std::move(spec));
      continue;
    }
    auto const alias = aliases.find(lowercase(item.column));
    if (alias != aliases.end()) {
      const OutputColumn& out = outputs[alias->second];
      spec.kind = out.kind == OutputColumn::Kind::kDistance
                      ? SortSpec::Kind::kDistance
                      : SortSpec::Kind::kColumn;
      spec.column = out.column;
      spec.query = out.query;
      sorts.push_back(std::move(spec));
      continue;
    }
    int const index = resolve_column(schema, item.column, "ORDER BY");
    if (schema.columns[static_cast<std::size_t>(index)].type.kind ==
        TypeKind::kVector) {
      throw SqlError("cannot order by vector column '" +
                     schema.columns[static_cast<std::size_t>(index)].name + "'");
    }
    spec.kind = SortSpec::Kind::kColumn;
    spec.column = index;
    sorts.push_back(std::move(spec));
  }

  bool const has_sort = !sorts.empty();
  std::size_t const limit =
      stmt.has_limit ? static_cast<std::size_t>(stmt.limit) : 0;
  Collector collector(&sorts, stmt.has_limit && has_sort, limit);

  std::vector<KeyValue> scratch;
  scratch.reserve(sorts.size());
  for_each_row(table, delta, [&](const RowRef& row) {
    if (!has_sort && stmt.has_limit && collector.size() >= limit) return;
    if (!terms.empty() && !row_matches(row, terms)) return;
    std::int64_t const pk = row.pk();
    if (!has_sort) {
      collector.consider_unsorted(pk);
      return;
    }
    scratch.clear();
    for (const SortSpec& spec : sorts) {
      scratch.push_back(key_for(row, spec, schema));
    }
    collector.consider(scratch, pk);
  });

  std::vector<Candidate> const selected = collector.finish();

  StatementResult result;
  result.columns.reserve(outputs.size());
  for (const OutputColumn& out : outputs) result.columns.push_back(out.name);
  result.rows.reserve(selected.size());
  for (const Candidate& candidate : selected) {
    std::vector<Value> const values = load_row(table, delta, candidate.pk);
    std::vector<Value> cells;
    cells.reserve(outputs.size());
    for (const OutputColumn& out : outputs) {
      if (out.kind == OutputColumn::Kind::kColumn) {
        cells.push_back(values[static_cast<std::size_t>(out.column)]);
      } else {
        cells.push_back(storage::l2_distance_vectors(
            std::get<Vector>(values[static_cast<std::size_t>(out.column)]).data,
            out.query));
      }
    }
    result.rows.push_back(std::move(cells));
  }
  return result;
}

// ---------- DDL ----------

StatementResult execute_create_table(const sql::CreateTableStmt& stmt,
                                     Catalog& catalog,
                                     std::vector<storage::WalRecord>* changes) {
  if (catalog.find(stmt.table) != nullptr) {
    throw SqlError("table '" + stmt.table + "' already exists");
  }

  Schema schema;
  schema.name = stmt.table;
  int pk_count = 0;
  int vector_count = 0;

  for (std::size_t i = 0; i < stmt.columns.size(); ++i) {
    const sql::ColumnDef& def = stmt.columns[i];
    for (std::size_t j = 0; j < i; ++j) {
      if (names_equal(stmt.columns[j].name, def.name)) {
        throw SqlError("duplicate column '" + def.name + "'");
      }
    }
    Column column;
    column.name = def.name;
    column.type = def.type;
    column.primary_key = def.primary_key;
    if (def.primary_key) {
      ++pk_count;
      schema.pk_index = static_cast<int>(i);
      if (def.type.kind != TypeKind::kBigInt) {
        throw SqlError("primary key column '" + def.name + "' must be BIGINT");
      }
    }
    if (def.type.kind == TypeKind::kVector) {
      ++vector_count;
      schema.vector_index = static_cast<int>(i);
    }
    schema.columns.push_back(std::move(column));
  }

  if (pk_count != 1) throw SqlError("exactly one primary key is required");
  if (vector_count > 1) {
    throw SqlError("at most one vector column is allowed");
  }

  changes->push_back(storage::CreateTableRecord{std::move(schema)});
  return StatementResult{};
}

StatementResult execute_create_index(const sql::CreateVectorIndexStmt& stmt,
                                     Catalog& catalog,
                                     std::vector<storage::WalRecord>* changes) {
  HeapTable& table = require_table(catalog, stmt.table);
  int const index = resolve_column(table.schema(), stmt.column, "vector index");
  if (table.schema().columns[static_cast<std::size_t>(index)].type.kind !=
      TypeKind::kVector) {
    throw SqlError("vector index column '" + stmt.column +
                   "' is not a VECTOR column");
  }
  if (!table.schema().vector_index_name.empty()) {
    throw SqlError("vector index already exists on table '" + stmt.table + "'");
  }
  changes->push_back(
      storage::CreateIndexRecord{table.schema().name, stmt.index});
  return StatementResult{};
}

// ---------- DML ----------

StatementResult execute_insert(const sql::InsertStmt& stmt, Catalog& catalog,
                               const TableOverlay* overlay,
                               std::vector<storage::WalRecord>* changes) {
  HeapTable& table = require_table(catalog, stmt.table);
  const Schema& schema = table.schema();
  std::size_t const column_count = schema.columns.size();

  std::vector<int> targets;
  if (stmt.columns.empty()) {
    targets.resize(column_count);
    std::iota(targets.begin(), targets.end(), 0);
  } else {
    for (const std::string& name : stmt.columns) {
      int const index = resolve_column(schema, name, "INSERT column list");
      for (int const existing : targets) {
        if (existing == index) {
          throw SqlError("duplicate column '" + name + "' in INSERT");
        }
      }
      targets.push_back(index);
    }
    if (targets.size() != column_count) {
      throw SqlError("all columns must be provided in INSERT");
    }
  }

  std::vector<std::vector<Value>> rows;
  rows.reserve(stmt.rows.size());
  std::unordered_set<std::int64_t> new_keys;

  for (const std::vector<Literal>& values : stmt.rows) {
    if (values.size() != targets.size()) {
      throw SqlError("column count doesn't match value count");
    }
    std::vector<Value> row(column_count);
    std::vector<bool> filled(column_count, false);
    for (std::size_t i = 0; i < targets.size(); ++i) {
      std::size_t const index = static_cast<std::size_t>(targets[i]);
      const Column& column = schema.columns[index];
      row[index] = value_from_literal(values[i], column.type, column.name);
      filled[index] = true;
    }
    for (std::size_t i = 0; i < column_count; ++i) {
      if (!filled[i]) {
        throw SqlError("column '" + schema.columns[i].name + "' is required");
      }
    }
    std::int64_t const key =
        std::get<std::int64_t>(row[static_cast<std::size_t>(schema.pk_index)]);
    if (pk_visible(table, overlay, key) || !new_keys.insert(key).second) {
      throw SqlError("duplicate primary key " + std::to_string(key));
    }
    rows.push_back(std::move(row));
  }

  if (!rows.empty()) {
    StatementResult result;
    result.has_affected_rows = true;
    result.affected_rows = rows.size();
    changes->push_back(storage::InsertRecord{schema.name, std::move(rows)});
    return result;
  }
  return StatementResult{};
}

StatementResult execute_update(const sql::UpdateStmt& stmt, Catalog& catalog,
                               const TableOverlay* overlay,
                               std::vector<storage::WalRecord>* changes) {
  HeapTable& table = require_table(catalog, stmt.table);
  const Schema& schema = table.schema();

  std::vector<std::pair<int, Value>> assignments;
  for (const auto& [name, literal] : stmt.assignments) {
    int const index = resolve_column(schema, name, "UPDATE");
    const Column& column = schema.columns[static_cast<std::size_t>(index)];
    if (column.primary_key) {
      throw SqlError("cannot update primary key column '" + column.name + "'");
    }
    assignments.emplace_back(
        index, value_from_literal(literal, column.type, column.name));
  }

  std::vector<BoundTerm> const terms =
      stmt.has_where ? bind_condition(stmt.where, schema)
                     : std::vector<BoundTerm>{};

  std::vector<std::int64_t> matches;
  for_each_row(table, overlay, [&](const RowRef& row) {
    if (!terms.empty() && !row_matches(row, terms)) return;
    matches.push_back(row.pk());
  });

  for (std::int64_t const key : matches) {
    changes->push_back(storage::UpdateRecord{schema.name, key, assignments});
  }
  StatementResult result;
  result.has_affected_rows = true;
  result.affected_rows = matches.size();
  return result;
}

StatementResult execute_delete(const sql::DeleteStmt& stmt, Catalog& catalog,
                               const TableOverlay* overlay,
                               std::vector<storage::WalRecord>* changes) {
  HeapTable& table = require_table(catalog, stmt.table);
  const Schema& schema = table.schema();

  std::vector<BoundTerm> const terms =
      stmt.has_where ? bind_condition(stmt.where, schema)
                     : std::vector<BoundTerm>{};

  std::vector<std::int64_t> matches;
  for_each_row(table, overlay, [&](const RowRef& row) {
    if (!terms.empty() && !row_matches(row, terms)) return;
    matches.push_back(row.pk());
  });

  for (std::int64_t const key : matches) {
    changes->push_back(storage::DeleteRecord{schema.name, key});
  }
  StatementResult result;
  result.has_affected_rows = true;
  result.affected_rows = matches.size();
  return result;
}

}  // namespace

StatementResult execute(const sql::Statement& statement, Catalog& catalog,
                        const OverlayMap* overlay,
                        std::vector<storage::WalRecord>* changes) {
  if (const auto* create = std::get_if<sql::CreateTableStmt>(&statement)) {
    return execute_create_table(*create, catalog, changes);
  }
  if (const auto* index = std::get_if<sql::CreateVectorIndexStmt>(&statement)) {
    return execute_create_index(*index, catalog, changes);
  }
  if (const auto* insert = std::get_if<sql::InsertStmt>(&statement)) {
    return execute_insert(*insert, catalog,
                          find_delta(overlay, insert->table), changes);
  }
  if (const auto* select = std::get_if<sql::SelectStmt>(&statement)) {
    return execute_select(*select, catalog, overlay);
  }
  if (const auto* update = std::get_if<sql::UpdateStmt>(&statement)) {
    return execute_update(*update, catalog,
                          find_delta(overlay, update->table), changes);
  }
  if (const auto* del = std::get_if<sql::DeleteStmt>(&statement)) {
    return execute_delete(*del, catalog, find_delta(overlay, del->table),
                          changes);
  }
  throw SqlError("internal error: unexpected statement type");
}

}  // namespace mvdb::exec
