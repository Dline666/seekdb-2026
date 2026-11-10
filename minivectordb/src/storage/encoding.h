#pragma once

#include <cstdint>
#include <string>
#include <string_view>

namespace mvdb::storage {

// 小端二进制写出器。所有多字节字段固定宽度，保证跨进程可解码。
class Writer {
 public:
  void u8(std::uint8_t value);
  void u32(std::uint32_t value);
  void u64(std::uint64_t value);
  void i64(std::int64_t value);
  void f32(float value);
  void bytes(std::string_view data);  // u32 长度 + 原始字节
  void write_raw(std::string_view data);  // 原始字节，不加长度前缀
  void reserve(std::size_t bytes) { buffer_.reserve(bytes); }

  const std::string& data() const { return buffer_; }

 private:
  std::string buffer_;
};

// 读取器。数据不足时抛 SqlError。
class Reader {
 public:
  explicit Reader(std::string_view data) : data_(data) {}

  std::uint8_t u8();
  std::uint32_t u32();
  std::uint64_t u64();
  std::int64_t i64();
  float f32();
  std::string bytes();
  std::string_view read_raw(std::size_t count);

  bool done() const { return pos_ == data_.size(); }
  void require_done() const;

 private:
  void need(std::size_t count) const;

  std::string_view data_;
  std::size_t pos_ = 0;
};

std::uint32_t crc32(std::string_view data);

}  // namespace mvdb::storage
