#include "common/value.h"

#include <charconv>
#include <cmath>
#include <string>
#include <system_error>
#include <type_traits>

#include "common/error.h"

namespace mvdb {
namespace {

bool is_ws(char c) {
  return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f' ||
         c == '\v';
}

std::string_view trim_ws(std::string_view text) {
  std::size_t begin = 0;
  std::size_t end = text.size();
  while (begin < end && is_ws(text[begin])) ++begin;
  while (end > begin && is_ws(text[end - 1])) --end;
  return text.substr(begin, end - begin);
}

double parse_double_token(std::string_view token) {
  if (token.empty()) throw SqlError("invalid vector literal: empty number");
  double value = 0.0;
  auto const result =
      std::from_chars(token.data(), token.data() + token.size(), value);
  if (result.ec != std::errc() || result.ptr != token.data() + token.size()) {
    throw SqlError("invalid number in vector literal: '" +
                   std::string(token) + "'");
  }
  if (!std::isfinite(value)) throw SqlError("vector value must be finite");
  return value;
}

template <typename T>
std::string format_fp(T value) {
  char buf[64];
  auto const result = std::to_chars(buf, buf + sizeof buf, value);
  if (result.ec != std::errc()) {
    return std::to_string(static_cast<double>(value));
  }
  return std::string(buf, result.ptr);
}

}  // namespace

std::vector<double> parse_vector_text(std::string_view text) {
  std::string_view const s = trim_ws(text);
  if (s.size() < 2 || s.front() != '[' || s.back() != ']') {
    throw SqlError("invalid vector literal: expected '[...]'");
  }
  std::string_view const inner = trim_ws(s.substr(1, s.size() - 2));
  std::vector<double> out;
  if (inner.empty()) return out;

  std::size_t pos = 0;
  for (;;) {
    std::size_t const comma = inner.find(',', pos);
    std::string_view const token = trim_ws(inner.substr(
        pos, comma == std::string_view::npos ? std::string_view::npos
                                             : comma - pos));
    out.push_back(parse_double_token(token));
    if (comma == std::string_view::npos) break;
    pos = comma + 1;
  }
  return out;
}

std::vector<float> to_vector_f32(const std::vector<double>& values,
                                 std::uint32_t dim) {
  if (values.size() != dim) {
    throw SqlError("vector dimension mismatch: expected " +
                   std::to_string(dim) + ", got " +
                   std::to_string(values.size()));
  }
  std::vector<float> out;
  out.reserve(values.size());
  for (double const v : values) {
    if (!std::isfinite(v)) throw SqlError("vector value must be finite");
    float const f = static_cast<float>(v);
    if (!std::isfinite(f)) throw SqlError("vector value out of float32 range");
    out.push_back(f);
  }
  return out;
}

std::vector<float> parse_vector_f32(std::string_view text, std::uint32_t dim) {
  return to_vector_f32(parse_vector_text(text), dim);
}

std::uint32_t utf8_length(const std::string& text) {
  std::uint32_t count = 0;
  for (unsigned char const c : text) {
    if ((c & 0xC0U) != 0x80U) ++count;
  }
  return count;
}

std::string format_double(double value) { return format_fp(value); }

std::string format_float(float value) { return format_fp(value); }

std::string value_to_string(const Value& value) {
  return std::visit(
      [](const auto& v) -> std::string {
        using T = std::decay_t<decltype(v)>;
        if constexpr (std::is_same_v<T, std::int64_t>) {
          return std::to_string(v);
        } else if constexpr (std::is_same_v<T, double>) {
          return format_double(v);
        } else if constexpr (std::is_same_v<T, std::string>) {
          return v;
        } else {
          std::string out = "[";
          for (std::size_t i = 0; i < v.data.size(); ++i) {
            if (i != 0) out += ", ";
            out += format_float(v.data[i]);
          }
          out += "]";
          return out;
        }
      },
      value);
}

}  // namespace mvdb
