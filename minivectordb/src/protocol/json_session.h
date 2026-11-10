#pragma once

#include <cstddef>
#include <cstdio>
#include <string>

#include "engine/engine.h"
#include "protocol/json.h"

namespace mvdb {

// JSON Lines 会话：一行一个请求，一行一个响应，每次输出后 flush。
class JsonSession {
 public:
  JsonSession(Engine& engine, std::FILE* in, std::FILE* out);

  // 处理请求直到 shutdown 或输入结束，返回进程退出码。
  int run();

 private:
  bool read_line(std::string* line);
  std::string handle(const json::Value& request, bool* stop);
  std::string render(const std::string& id, const StatementResult& result);

  Engine& engine_;
  std::FILE* in_;
  std::FILE* out_;
  std::string buffer_;
  std::size_t pos_ = 0;
  bool eof_ = false;
};

}  // namespace mvdb
