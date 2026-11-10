#pragma once

#include <cstddef>
#include <cstdint>
#include <string>
#include <string_view>
#include <vector>

namespace mvdb::sql {

enum class TokenKind {
  kIdent,
  kNumber,
  kString,
  kVector,
  kSymbol,
  kEnd,
};

struct Token {
  TokenKind kind = TokenKind::kEnd;
  std::string text;  // ident 原文 / symbol 文本 / 解转义后的字符串
  std::int64_t number = 0;
  std::vector<double> vector;
  std::size_t pos = 0;
};

// 词法分析：输入是一条已按分号切开、已去除注释的语句。失败抛 SqlError。
std::vector<Token> tokenize(std::string_view sql);

bool equal_ci(std::string_view lhs, std::string_view rhs);

}  // namespace mvdb::sql
