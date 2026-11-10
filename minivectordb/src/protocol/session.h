#pragma once

#include <cstdio>

#include "engine/engine.h"
#include "protocol/formatter.h"

namespace mvdb {

// stdin/stdout 会话：读语句、执行、排版、写出，并逐条刷新。
class Session {
 public:
  Session(Engine& engine, const Formatter& formatter, std::FILE* out);

  // 处理输入直到 EOF，返回进程退出码。
  int run();

 private:
  Engine& engine_;
  const Formatter& formatter_;
  std::FILE* out_;
};

}  // namespace mvdb
