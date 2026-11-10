#pragma once

#include <cstdint>
#include <string>
#include <string_view>
#include <utility>
#include <vector>

namespace mvdb::json {

// 最小的 JSON 值表示：只覆盖协议需要的对象/数组/字符串/数字/布尔/null。
struct Value {
  enum class Type { kNull, kBool, kNumber, kString, kArray, kObject };

  Type type = Type::kNull;
  bool boolean = false;
  double number = 0.0;
  std::string raw;  // 标量的原始 JSON 文本（含引号），用于原样回传 id
  std::string string;
  std::vector<Value> array;
  std::vector<std::pair<std::string, Value>> object;

  const Value* find(std::string_view key) const;
};

// 解析一个完整 JSON 文档；失败抛 SqlError。
Value parse(std::string_view text);

// 序列化辅助。
std::string quote(std::string_view text);
std::string number_to_json(double value);
std::string float_to_json(float value);

}  // namespace mvdb::json
