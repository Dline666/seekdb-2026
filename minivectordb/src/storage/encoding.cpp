#include "storage/encoding.h"

#include <array>
#include <cstring>

#include "common/error.h"

namespace mvdb::storage {

void Writer::u8(std::uint8_t value) {
  buffer_.push_back(static_cast<char>(value));
}

void Writer::u32(std::uint32_t value) {
  char encoded[4];
  for (int i = 0; i < 4; ++i) {
    encoded[i] = static_cast<char>((value >> (8 * i)) & 0xFFU);
  }
  buffer_.append(encoded, sizeof encoded);
}

void Writer::u64(std::uint64_t value) {
  char encoded[8];
  for (int i = 0; i < 8; ++i) {
    encoded[i] = static_cast<char>((value >> (8 * i)) & 0xFFU);
  }
  buffer_.append(encoded, sizeof encoded);
}

void Writer::i64(std::int64_t value) {
  u64(static_cast<std::uint64_t>(value));
}

void Writer::f32(float value) {
  std::uint32_t bits = 0;
  std::memcpy(&bits, &value, sizeof bits);
  u32(bits);
}

void Writer::bytes(std::string_view data) {
  u32(static_cast<std::uint32_t>(data.size()));
  buffer_.append(data.data(), data.size());
}

void Writer::write_raw(std::string_view data) {
  buffer_.append(data.data(), data.size());
}

void Reader::need(std::size_t count) const {
  if (pos_ + count > data_.size()) {
    throw SqlError("corrupt data: unexpected end of record");
  }
}

std::uint8_t Reader::u8() {
  need(1);
  return static_cast<std::uint8_t>(
      static_cast<unsigned char>(data_[pos_++]));
}

std::uint32_t Reader::u32() {
  need(4);
  std::uint32_t value = 0;
  for (int i = 0; i < 4; ++i) {
    value |= static_cast<std::uint32_t>(
                 static_cast<unsigned char>(data_[pos_ + static_cast<std::size_t>(i)]))
             << (8 * i);
  }
  pos_ += 4;
  return value;
}

std::uint64_t Reader::u64() {
  need(8);
  std::uint64_t value = 0;
  for (int i = 0; i < 8; ++i) {
    value |= static_cast<std::uint64_t>(
                 static_cast<unsigned char>(data_[pos_ + static_cast<std::size_t>(i)]))
             << (8 * i);
  }
  pos_ += 8;
  return value;
}

std::int64_t Reader::i64() {
  return static_cast<std::int64_t>(u64());
}

float Reader::f32() {
  std::uint32_t const bits = u32();
  float value = 0.0F;
  std::memcpy(&value, &bits, sizeof value);
  return value;
}

std::string Reader::bytes() {
  std::uint32_t const length = u32();
  need(length);
  std::string out(data_.substr(pos_, length));
  pos_ += length;
  return out;
}

void Reader::require_done() const {
  if (!done()) throw SqlError("corrupt data: trailing bytes in record");
}

std::string_view Reader::read_raw(std::size_t count) {
  need(count);
  std::string_view const out = data_.substr(pos_, count);
  pos_ += count;
  return out;
}

std::uint32_t crc32(std::string_view data) {
  static const std::array<std::uint32_t, 256> table = [] {
    std::array<std::uint32_t, 256> values{};
    for (std::uint32_t i = 0; i < 256; ++i) {
      std::uint32_t value = i;
      for (int bit = 0; bit < 8; ++bit) {
        value = (value & 1U) != 0U ? (value >> 1) ^ 0xEDB88320U : value >> 1;
      }
      values[i] = value;
    }
    return values;
  }();

  std::uint32_t crc = 0xFFFFFFFFU;
  for (char const c : data) {
    auto const index = static_cast<std::size_t>((crc ^ static_cast<std::uint32_t>(
                                                          static_cast<unsigned char>(c))) &
                                                0xFFU);
    crc = table[index] ^ (crc >> 8);
  }
  return crc ^ 0xFFFFFFFFU;
}

}  // namespace mvdb::storage
