#include "protocol/statement_reader.h"

#include <cerrno>
#include <utility>

#ifdef _WIN32
#include <io.h>
#define MVDB_READ ::_read
#else
#include <unistd.h>
#define MVDB_READ ::read
#endif

namespace mvdb {
namespace {

constexpr std::size_t kReadBlock = 64 * 1024;

bool is_space(char c) {
  return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f' ||
         c == '\v';
}

bool is_blank(const std::string& text) {
  for (char c : text) {
    if (!is_space(c)) return false;
  }
  return true;
}

std::string trim_copy(const std::string& text) {
  std::size_t begin = 0;
  std::size_t end = text.size();
  while (begin < end && is_space(text[begin])) ++begin;
  while (end > begin && is_space(text[end - 1])) --end;
  return text.substr(begin, end - begin);
}

}  // namespace

StatementReader::StatementReader(
    int fd, std::function<bool(std::string_view)> is_complete)
    : fd_(fd), is_complete_(std::move(is_complete)) {}

bool StatementReader::has_partial_at_eof() const {
  return eof_ && !is_blank(partial_);
}

int StatementReader::peek(std::size_t ahead) {
  while (buffer_.size() <= pos_ + ahead) {
    if (eof_) return -1;
    // 丢弃已消费前缀，保证缓冲区只保留未处理的数据。
    if (pos_ > 0) {
      buffer_.erase(0, pos_);
      pos_ = 0;
    }
    std::size_t const old_size = buffer_.size();
    buffer_.resize(old_size + kReadBlock);
    auto const n = MVDB_READ(fd_, buffer_.data() + old_size, kReadBlock);
    if (n < 0) {
      buffer_.resize(old_size);
      if (errno == EINTR) return -1;  // 被信号打断：交给上层结束会话
      eof_ = true;
      return -1;
    }
    if (n == 0) {
      buffer_.resize(old_size);
      eof_ = true;
      return -1;
    }
    buffer_.resize(old_size + static_cast<std::size_t>(n));
  }
  return static_cast<unsigned char>(buffer_[pos_ + ahead]);
}

int StatementReader::get() {
  int const c = peek();
  if (c < 0) return -1;
  ++pos_;
  if (pos_ >= buffer_.size()) {
    buffer_.clear();
    pos_ = 0;
  }
  return c;
}

bool StatementReader::next(std::string* statement) {
  for (;;) {
    int const raw = get();
    if (raw < 0) {
      // 输入结束：把残留内容作为最后一条语句交给上层判断。
      if (!is_blank(partial_)) {
        *statement = trim_copy(partial_);
        partial_.clear();
        return true;
      }
      return false;
    }
    char const c = static_cast<char>(raw);

    switch (state_) {
      case State::kNormal:
        if (c == '\'') {
          partial_.push_back(c);
          state_ = State::kSingleQuote;
        } else if (c == '"') {
          partial_.push_back(c);
          state_ = State::kDoubleQuote;
        } else if (c == '[') {
          partial_.push_back(c);
          state_ = State::kBracket;
        } else if (c == '\n') {
          // 单行语句可以换行结束；已经跨行的语句仍等待分号。
          if (!is_blank(partial_) &&
              partial_.find('\n') == std::string::npos && is_complete_ &&
              is_complete_(partial_)) {
            *statement = trim_copy(partial_);
            partial_.clear();
            return true;
          }
          partial_.push_back('\n');
        } else if (c == '#') {
          state_ = State::kLineComment;
        } else if (c == '-' && peek() == '-') {
          // MySQL 约定：-- 后必须跟空白才构成注释，避免把 a- -b 误判。
          int const after = peek(1);
          if (after < 0 || is_space(static_cast<char>(after))) {
            get();
            state_ = State::kLineComment;
          } else {
            partial_.push_back(c);
          }
        } else if (c == '/' && peek() == '*') {
          get();
          state_ = State::kBlockComment;
        } else if (c == ';') {
          if (!is_blank(partial_)) {
            *statement = trim_copy(partial_);
            partial_.clear();
            return true;
          }
          partial_.clear();
        } else {
          partial_.push_back(c);
        }
        break;

      case State::kSingleQuote:
        partial_.push_back(c);
        if (c == '\\') {
          int const escaped = get();
          if (escaped >= 0) partial_.push_back(static_cast<char>(escaped));
        } else if (c == '\'') {
          if (peek() == '\'') {
            partial_.push_back(static_cast<char>(get()));  // '' 转义
          } else {
            state_ = State::kNormal;
          }
        }
        break;

      case State::kDoubleQuote:
        partial_.push_back(c);
        if (c == '\\') {
          int const escaped = get();
          if (escaped >= 0) partial_.push_back(static_cast<char>(escaped));
        } else if (c == '"') {
          if (peek() == '"') {
            partial_.push_back(static_cast<char>(get()));
          } else {
            state_ = State::kNormal;
          }
        }
        break;

      case State::kBracket:
        partial_.push_back(c);
        if (c == ']') state_ = State::kNormal;
        break;

      case State::kLineComment:
        if (c == '\n') {
          partial_.push_back(' ');
          state_ = State::kNormal;
        }
        break;

      case State::kBlockComment:
        if (c == '*' && peek() == '/') {
          get();
          partial_.push_back(' ');
          state_ = State::kNormal;
        }
        break;
    }
  }
}

}  // namespace mvdb
