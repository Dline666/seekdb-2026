#pragma once

#include <exception>
#include <string>
#include <utility>

namespace mvdb {

// 所有可预期的 SQL 错误都在这里抛出，由 Engine 边界统一转成错误结果。
class SqlError : public std::exception {
 public:
  explicit SqlError(std::string message) : message_(std::move(message)) {}

  const char* what() const noexcept override { return message_.c_str(); }

 private:
  std::string message_;
};

}  // namespace mvdb
