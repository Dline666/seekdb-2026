#pragma once

#include <cstdint>
#include <string>
#include <string_view>
#include <variant>
#include <vector>

namespace mvdb {

enum class TypeKind {
  kBigInt,
  kVarchar,
  kVector,
};

struct ColumnType {
  TypeKind kind = TypeKind::kBigInt;
  std::uint32_t size = 0;  // VARCHAR 最大字符数 / VECTOR 维度
};

struct Vector {
  std::vector<float> data;
};

// 行值：BIGINT / VARCHAR / VECTOR / 查询中计算出的距离（double）。
using Value = std::variant<std::int64_t, double, std::string, Vector>;

// 解析 "[1, 2, 3]" 形式的向量文本，失败抛 SqlError。
std::vector<double> parse_vector_text(std::string_view text);

// 把 double 序列校验并转为 float32：维度必须等于 dim，拒绝 NaN/Inf/溢出。
std::vector<float> to_vector_f32(const std::vector<double>& values,
                                 std::uint32_t dim);

// 解析文本并校验为 float32 向量。
std::vector<float> parse_vector_f32(std::string_view text, std::uint32_t dim);

// UTF-8 字符串的码点个数（用作 VARCHAR 长度）。
std::uint32_t utf8_length(const std::string& text);

std::string value_to_string(const Value& value);
std::string format_double(double value);
std::string format_float(float value);

}  // namespace mvdb
