#pragma once

#include <cstdint>
#include <string>
#include <vector>

#include "common/value.h"

namespace mvdb {

// 语句执行状态。题包错误码映射未冻结时，error_code 只需非空。
enum class Status {
  kOk,
  kError,
  kNotImplemented,
};

// 执行器与协议层之间的唯一数据通道。
// 行值保持类型，由协议层决定序列化为 JSON 还是文本。
struct StatementResult {
  Status status = Status::kOk;
  // 仅 INSERT/UPDATE/DELETE 为真；JSON 响应据此决定是否输出 affected_rows。
  bool has_affected_rows = false;
  std::uint64_t affected_rows = 0;
  std::vector<std::string> columns;
  std::vector<std::vector<Value>> rows;
  std::string message;
  std::string error_code;
};

}  // namespace mvdb
