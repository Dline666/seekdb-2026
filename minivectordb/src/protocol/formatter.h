#pragma once

#include <string>
#include <string_view>

#include "common/result.h"

namespace mvdb {

// 结果排版。题包发布后只需替换本接口的实现，执行层不受影响。
class Formatter {
 public:
  virtual ~Formatter() = default;
  virtual std::string format(std::string_view sql,
                             const StatementResult& result) const = 0;
};

// 表格协议（P1 临时格式，自描述以便驱动解析）：
//   查询：首行 "ROWS <n>"，随后是表头与 n 行数据，均用制表符分隔；
//   非查询成功："OK"；
//   错误："ERR <message>"。
class TabularFormatter final : public Formatter {
 public:
  std::string format(std::string_view sql,
                     const StatementResult& result) const override;
};

// 回显协议：调试用，回显语句并给出状态。
class EchoFormatter final : public Formatter {
 public:
  std::string format(std::string_view sql,
                     const StatementResult& result) const override;
};

}  // namespace mvdb
