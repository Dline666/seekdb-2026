#pragma once

#include <string_view>

#include "sql/ast.h"

namespace mvdb::sql {

// 解析一条语句（已去除结尾分号）。不支持的语句与语法错误都抛 SqlError。
Statement parse(std::string_view sql);

}  // namespace mvdb::sql
