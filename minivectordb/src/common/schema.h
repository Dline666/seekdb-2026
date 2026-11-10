#pragma once

#include <cctype>
#include <cstdint>
#include <string>
#include <string_view>
#include <vector>

#include "common/value.h"

namespace mvdb {

struct Column {
  std::string name;
  ColumnType type;
  bool primary_key = false;
};

struct Schema {
  std::string name;
  std::vector<Column> columns;
  int pk_index = -1;
  int vector_index = -1;
  std::string vector_index_name;

  // 列名按大小写不敏感匹配，找不到返回 -1。
  int find_column(std::string_view name) const;
};

bool names_equal(std::string_view lhs, std::string_view rhs);
std::string lowercase(std::string_view text);

}  // namespace mvdb
