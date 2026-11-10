#include "sql/lexer.h"

#include <cctype>
#include <charconv>
#include <string>
#include <string_view>
#include <system_error>

#include "common/error.h"
#include "common/value.h"

namespace mvdb::sql {
namespace {

bool is_ident_start(unsigned char c) {
  return std::isalpha(c) != 0 || c == '_';
}

bool is_ident_char(unsigned char c) {
  return std::isalnum(c) != 0 || c == '_' || c == '$';
}

bool is_digit(unsigned char c) { return c >= '0' && c <= '9'; }

bool is_space(unsigned char c) {
  return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f' ||
         c == '\v';
}

std::string escape_value(char escaped) {
  switch (escaped) {
    case '0':
      return std::string(1, '\0');
    case 'b':
      return std::string(1, '\b');
    case 'n':
      return std::string(1, '\n');
    case 'r':
      return std::string(1, '\r');
    case 't':
      return std::string(1, '\t');
    case 'Z':
      return std::string(1, '\x1a');
    case '%':
      return "\\%";
    case '_':
      return "\\_";
    default:
      return std::string(1, escaped);
  }
}

}  // namespace

bool equal_ci(std::string_view lhs, std::string_view rhs) {
  if (lhs.size() != rhs.size()) return false;
  for (std::size_t i = 0; i < lhs.size(); ++i) {
    unsigned char const a = static_cast<unsigned char>(lhs[i]);
    unsigned char const b = static_cast<unsigned char>(rhs[i]);
    if (std::tolower(a) != std::tolower(b)) return false;
  }
  return true;
}

std::vector<Token> tokenize(std::string_view sql) {
  std::vector<Token> out;
  std::size_t i = 0;
  std::size_t const n = sql.size();

  while (i < n) {
    unsigned char const c = static_cast<unsigned char>(sql[i]);
    if (is_space(c)) {
      ++i;
      continue;
    }
    std::size_t const start = i;

    if (is_ident_start(c)) {
      while (i < n && is_ident_char(static_cast<unsigned char>(sql[i]))) ++i;
      Token token;
      token.kind = TokenKind::kIdent;
      token.text = std::string(sql.substr(start, i - start));
      token.pos = start;
      out.push_back(std::move(token));
      continue;
    }

    if (is_digit(c)) {
      while (i < n && is_digit(static_cast<unsigned char>(sql[i]))) ++i;
      if (i < n && sql[i] == '.') {
        throw SqlError("syntax error: decimal literal is not supported");
      }
      std::string_view const text = sql.substr(start, i - start);
      std::uint64_t value = 0;
      auto const result =
          std::from_chars(text.data(), text.data() + text.size(), value);
      if (result.ec == std::errc::result_out_of_range) {
        throw SqlError("integer literal out of range: " + std::string(text));
      }
      if (result.ec != std::errc() ||
          result.ptr != text.data() + text.size()) {
        throw SqlError("invalid integer literal: " + std::string(text));
      }
      Token token;
      token.kind = TokenKind::kNumber;
      token.magnitude = value;
      token.text = std::string(text);
      token.pos = start;
      out.push_back(std::move(token));
      continue;
    }

    if (sql[i] == '\'') {
      ++i;
      std::string value;
      bool closed = false;
      while (i < n) {
        char const ch = sql[i];
        if (ch == '\'') {
          if (i + 1 < n && sql[i + 1] == '\'') {
            value.push_back('\'');
            i += 2;
            continue;
          }
          ++i;
          closed = true;
          break;
        }
        if (ch == '\\') {
          if (i + 1 >= n) throw SqlError("unterminated string literal");
          value += escape_value(sql[i + 1]);
          i += 2;
          continue;
        }
        value.push_back(ch);
        ++i;
      }
      if (!closed) throw SqlError("unterminated string literal");
      Token token;
      token.kind = TokenKind::kString;
      token.text = std::move(value);
      token.pos = start;
      out.push_back(std::move(token));
      continue;
    }

    if (sql[i] == '[') {
      std::size_t const close = sql.find(']', i + 1);
      if (close == std::string_view::npos) {
        throw SqlError("unterminated vector literal");
      }
      std::string const text(sql.substr(i, close - i + 1));
      Token token;
      token.kind = TokenKind::kVector;
      token.vector = parse_vector_text(text);
      token.text = text;
      token.pos = start;
      out.push_back(std::move(token));
      i = close + 1;
      continue;
    }

    char const ch = sql[i];
    std::string symbol;
    if (ch == '<' && i + 1 < n && sql[i + 1] == '=') {
      symbol = "<=";
      i += 2;
    } else if (ch == '>' && i + 1 < n && sql[i + 1] == '=') {
      symbol = ">=";
      i += 2;
    } else if (ch == '!') {
      throw SqlError("syntax error: '!' operator is not supported");
    } else if (ch == '-' || ch == '=' || ch == '<' || ch == '>' || ch == '(' ||
               ch == ')' || ch == ',' || ch == '*' || ch == '.' || ch == '+') {
      symbol = std::string(1, ch);
      ++i;
    } else {
      throw SqlError(std::string("syntax error: unexpected character '") + ch +
                     "'");
    }
    Token token;
    token.kind = TokenKind::kSymbol;
    token.text = std::move(symbol);
    token.pos = start;
    out.push_back(std::move(token));
  }

  Token end;
  end.kind = TokenKind::kEnd;
  end.pos = n;
  out.push_back(std::move(end));
  return out;
}

}  // namespace mvdb::sql
