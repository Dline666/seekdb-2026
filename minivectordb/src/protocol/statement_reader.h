#pragma once

#include <cstddef>
#include <functional>
#include <string>
#include <string_view>

namespace mvdb {

// 从 fd 增量读取 SQL 文本，按分号切分完整语句。
// 单引号/双引号字符串、方括号向量字面量以及注释内的分号不视为边界。
// 注释被丢弃并以一个空格代替，避免两侧 token 粘连。
//
// 另支持"单行语句"：换行时若当前累积内容没有换行且 is_complete 判定为完整，
// 则以换行作为语句边界。多行语句仍以分号结束。
class StatementReader {
 public:
  explicit StatementReader(
      int fd, std::function<bool(std::string_view)> is_complete = nullptr);

  StatementReader(const StatementReader&) = delete;
  StatementReader& operator=(const StatementReader&) = delete;

  // 读到下一条完整语句（去掉结尾分号与首尾空白）时返回 true。
  // EOF 时若仍有非空白残留，则把残留作为最后一条语句返回。
  bool next(std::string* statement);

  bool has_partial_at_eof() const;

 private:
  enum class State {
    kNormal,
    kSingleQuote,
    kDoubleQuote,
    kBracket,
    kLineComment,
    kBlockComment,
  };

  int get();
  int peek(std::size_t ahead = 0);

  int fd_;
  bool eof_ = false;
  std::string buffer_;  // 已读入但尚未消费的字节
  std::size_t pos_ = 0;
  std::string partial_;  // 当前语句已累积的内容
  State state_ = State::kNormal;
  std::function<bool(std::string_view)> is_complete_;
};

}  // namespace mvdb
