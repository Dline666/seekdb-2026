#include "protocol/json.h"

#include <charconv>
#include <cmath>
#include <cstdio>
#include <cstdint>
#include <string>
#include <string_view>
#include <system_error>
#include <utility>

#include "common/error.h"

namespace mvdb::json {
namespace {

bool is_digit(char c) { return c >= '0' && c <= '9'; }

int hex_value(char c) {
  if (c >= '0' && c <= '9') return c - '0';
  if (c >= 'a' && c <= 'f') return c - 'a' + 10;
  if (c >= 'A' && c <= 'F') return c - 'A' + 10;
  return -1;
}

void append_utf8(std::uint32_t code, std::string* out) {
  if (code <= 0x7F) {
    out->push_back(static_cast<char>(code));
  } else if (code <= 0x7FF) {
    out->push_back(static_cast<char>(0xC0 | (code >> 6)));
    out->push_back(static_cast<char>(0x80 | (code & 0x3F)));
  } else if (code <= 0xFFFF) {
    out->push_back(static_cast<char>(0xE0 | (code >> 12)));
    out->push_back(static_cast<char>(0x80 | ((code >> 6) & 0x3F)));
    out->push_back(static_cast<char>(0x80 | (code & 0x3F)));
  } else {
    out->push_back(static_cast<char>(0xF0 | (code >> 18)));
    out->push_back(static_cast<char>(0x80 | ((code >> 12) & 0x3F)));
    out->push_back(static_cast<char>(0x80 | ((code >> 6) & 0x3F)));
    out->push_back(static_cast<char>(0x80 | (code & 0x3F)));
  }
}

class Parser {
 public:
  explicit Parser(std::string_view text) : text_(text) {}

  Value parse_document() {
    Value value = parse_value();
    skip_ws();
    if (pos_ != text_.size()) fail("trailing characters");
    return value;
  }

 private:
  [[noreturn]] void fail(const std::string& why) const {
    throw SqlError("invalid JSON: " + why + " at offset " +
                   std::to_string(pos_));
  }

  void skip_ws() {
    while (pos_ < text_.size()) {
      char const c = text_[pos_];
      if (c != ' ' && c != '\t' && c != '\r' && c != '\n') break;
      ++pos_;
    }
  }

  char peek() const {
    if (pos_ >= text_.size()) fail("unexpected end");
    return text_[pos_];
  }

  void expect(char c) {
    if (pos_ >= text_.size() || text_[pos_] != c) {
      fail(std::string("expected '") + c + "'");
    }
    ++pos_;
  }

  bool consume(std::string_view literal) {
    if (text_.substr(pos_, literal.size()) == literal) {
      pos_ += literal.size();
      return true;
    }
    return false;
  }

  Value parse_value() {
    skip_ws();
    if (pos_ >= text_.size()) fail("unexpected end");
    char const c = text_[pos_];
    if (c == '{') return parse_object();
    if (c == '[') return parse_array();
    if (c == '"') return parse_string_value();
    if (c == 't') {
      if (!consume("true")) fail("bad literal");
      Value value;
      value.type = Value::Type::kBool;
      value.boolean = true;
      value.raw = "true";
      return value;
    }
    if (c == 'f') {
      if (!consume("false")) fail("bad literal");
      Value value;
      value.type = Value::Type::kBool;
      value.boolean = false;
      value.raw = "false";
      return value;
    }
    if (c == 'n') {
      if (!consume("null")) fail("bad literal");
      Value value;
      value.type = Value::Type::kNull;
      value.raw = "null";
      return value;
    }
    if (c == '-' || is_digit(c)) return parse_number();
    fail("unexpected character");
  }

  Value parse_object() {
    Value value;
    value.type = Value::Type::kObject;
    expect('{');
    skip_ws();
    if (peek() == '}') {
      ++pos_;
      return value;
    }
    for (;;) {
      skip_ws();
      if (peek() != '"') fail("expected object key");
      std::string key = parse_string();
      skip_ws();
      expect(':');
      value.object.emplace_back(std::move(key), parse_value());
      skip_ws();
      if (peek() == ',') {
        ++pos_;
        continue;
      }
      expect('}');
      break;
    }
    return value;
  }

  Value parse_array() {
    Value value;
    value.type = Value::Type::kArray;
    expect('[');
    skip_ws();
    if (peek() == ']') {
      ++pos_;
      return value;
    }
    for (;;) {
      value.array.push_back(parse_value());
      skip_ws();
      if (peek() == ',') {
        ++pos_;
        continue;
      }
      expect(']');
      break;
    }
    return value;
  }

  Value parse_string_value() {
    std::size_t const start = pos_;
    std::string text = parse_string();
    Value value;
    value.type = Value::Type::kString;
    value.string = std::move(text);
    value.raw = std::string(text_.substr(start, pos_ - start));
    return value;
  }

  std::uint32_t parse_hex4() {
    std::uint32_t value = 0;
    for (int i = 0; i < 4; ++i) {
      if (pos_ >= text_.size()) fail("bad unicode escape");
      int const digit = hex_value(text_[pos_++]);
      if (digit < 0) fail("bad unicode escape");
      value = (value << 4) | static_cast<std::uint32_t>(digit);
    }
    return value;
  }

  std::string parse_string() {
    expect('"');
    std::string out;
    for (;;) {
      if (pos_ >= text_.size()) fail("unterminated string");
      unsigned char const c = static_cast<unsigned char>(text_[pos_]);
      if (c == '"') {
        ++pos_;
        break;
      }
      if (c == '\\') {
        ++pos_;
        if (pos_ >= text_.size()) fail("bad escape");
        char const escaped = text_[pos_++];
        switch (escaped) {
          case '"':
            out.push_back('"');
            break;
          case '\\':
            out.push_back('\\');
            break;
          case '/':
            out.push_back('/');
            break;
          case 'b':
            out.push_back('\b');
            break;
          case 'f':
            out.push_back('\f');
            break;
          case 'n':
            out.push_back('\n');
            break;
          case 'r':
            out.push_back('\r');
            break;
          case 't':
            out.push_back('\t');
            break;
          case 'u': {
            std::uint32_t code = parse_hex4();
            if (code >= 0xD800 && code <= 0xDBFF) {
              if (!consume("\\u")) fail("bad surrogate pair");
              std::uint32_t const low = parse_hex4();
              if (low < 0xDC00 || low > 0xDFFF) fail("bad surrogate pair");
              code = 0x10000 + ((code - 0xD800) << 10) + (low - 0xDC00);
            }
            append_utf8(code, &out);
            break;
          }
          default:
            fail("bad escape");
        }
        continue;
      }
      if (c < 0x20) fail("control character in string");
      out.push_back(static_cast<char>(c));
      ++pos_;
    }
    return out;
  }

  Value parse_number() {
    std::size_t const start = pos_;
    if (text_[pos_] == '-') ++pos_;
    if (pos_ >= text_.size() || !is_digit(text_[pos_])) fail("bad number");
    while (pos_ < text_.size() && is_digit(text_[pos_])) ++pos_;
    if (pos_ < text_.size() && text_[pos_] == '.') {
      ++pos_;
      if (pos_ >= text_.size() || !is_digit(text_[pos_])) fail("bad number");
      while (pos_ < text_.size() && is_digit(text_[pos_])) ++pos_;
    }
    if (pos_ < text_.size() && (text_[pos_] == 'e' || text_[pos_] == 'E')) {
      ++pos_;
      if (pos_ < text_.size() && (text_[pos_] == '+' || text_[pos_] == '-')) {
        ++pos_;
      }
      if (pos_ >= text_.size() || !is_digit(text_[pos_])) fail("bad number");
      while (pos_ < text_.size() && is_digit(text_[pos_])) ++pos_;
    }
    std::string_view const token = text_.substr(start, pos_ - start);
    double value = 0.0;
    auto const result =
        std::from_chars(token.data(), token.data() + token.size(), value);
    if (result.ec != std::errc() ||
        result.ptr != token.data() + token.size() || !std::isfinite(value)) {
      fail("number out of range");
    }
    Value out;
    out.type = Value::Type::kNumber;
    out.number = value;
    out.raw = std::string(token);
    return out;
  }

  std::string_view text_;
  std::size_t pos_ = 0;
};

}  // namespace

const Value* Value::find(std::string_view key) const {
  for (const auto& [name, value] : object) {
    if (name == key) return &value;
  }
  return nullptr;
}

Value parse(std::string_view text) { return Parser(text).parse_document(); }

std::string quote(std::string_view text) {
  std::string out;
  out.reserve(text.size() + 2);
  out.push_back('"');
  for (unsigned char const c : text) {
    switch (c) {
      case '"':
        out += "\\\"";
        break;
      case '\\':
        out += "\\\\";
        break;
      case '\b':
        out += "\\b";
        break;
      case '\f':
        out += "\\f";
        break;
      case '\n':
        out += "\\n";
        break;
      case '\r':
        out += "\\r";
        break;
      case '\t':
        out += "\\t";
        break;
      default:
        if (c < 0x20) {
          char buffer[8];
          std::snprintf(buffer, sizeof buffer, "\\u%04x",
                        static_cast<unsigned int>(c));
          out += buffer;
        } else {
          out.push_back(static_cast<char>(c));
        }
    }
  }
  out.push_back('"');
  return out;
}

std::string number_to_json(double value) {
  if (!std::isfinite(value)) {
    throw SqlError("cannot serialize non-finite number");
  }
  char buffer[64];
  auto const result = std::to_chars(buffer, buffer + sizeof buffer, value);
  if (result.ec != std::errc()) throw SqlError("cannot format number");
  return std::string(buffer, result.ptr);
}

std::string float_to_json(float value) {
  if (!std::isfinite(value)) {
    throw SqlError("cannot serialize non-finite number");
  }
  char buffer[64];
  auto const result = std::to_chars(buffer, buffer + sizeof buffer, value);
  if (result.ec != std::errc()) throw SqlError("cannot format number");
  return std::string(buffer, result.ptr);
}

}  // namespace mvdb::json
