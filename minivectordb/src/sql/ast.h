#pragma once

#include <cstdint>
#include <string>
#include <utility>
#include <variant>
#include <vector>

#include "common/value.h"

namespace mvdb::sql {

struct Literal {
  enum class Kind {
    kInt,
    kString,
    kVector,
    kNull,
  };

  Kind kind = Kind::kNull;
  std::int64_t int_value = 0;
  std::string text;            // kString 解转义后的内容
  std::vector<double> vector;  // kVector 未加引号的 [..] 字面量
};

struct ColumnDef {
  std::string name;
  ColumnType type;
  bool primary_key = false;
};

struct CreateTableStmt {
  std::string table;
  std::vector<ColumnDef> columns;
};

struct CreateVectorIndexStmt {
  std::string index;
  std::string table;
  std::string column;
};

struct InsertStmt {
  std::string table;
  std::vector<std::string> columns;  // 为空表示按表定义顺序
  std::vector<std::vector<Literal>> rows;
};

enum class CompareOp {
  kEq,
  kLt,
  kLe,
  kGt,
  kGe,
};

struct Condition {
  struct Term {
    std::string column;
    CompareOp op = CompareOp::kEq;
    Literal literal;
    bool column_on_left = true;
  };
  std::vector<Term> terms;  // 各项之间为 AND
};

struct SelectItem {
  bool star = false;
  std::string column;
  bool is_distance = false;
  Literal query;  // L2_DISTANCE 的向量参数
  std::string alias;
};

struct OrderItem {
  bool is_distance = false;
  std::string column;  // 普通列名或输出别名
  Literal query;
  bool descending = false;
};

struct SelectStmt {
  std::vector<SelectItem> items;
  std::string table;
  bool has_where = false;
  Condition where;
  std::vector<OrderItem> order_by;
  bool has_limit = false;
  std::int64_t limit = 0;
};

struct UpdateStmt {
  std::string table;
  std::vector<std::pair<std::string, Literal>> assignments;
  bool has_where = false;
  Condition where;
};

struct DeleteStmt {
  std::string table;
  bool has_where = false;
  Condition where;
};

struct TransactionStmt {
  enum class Kind { kBegin, kCommit, kRollback };
  Kind kind = Kind::kBegin;
};

using Statement =
    std::variant<CreateTableStmt, CreateVectorIndexStmt, InsertStmt, SelectStmt,
                 UpdateStmt, DeleteStmt, TransactionStmt>;

}  // namespace mvdb::sql
