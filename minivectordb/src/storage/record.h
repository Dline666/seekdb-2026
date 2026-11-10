#pragma once

#include <cstdint>
#include <string>
#include <string_view>
#include <utility>
#include <vector>

#include "common/schema.h"
#include "common/value.h"
#include "storage/encoding.h"

namespace mvdb::storage {

struct Rid {
  std::uint32_t page = 0;
  std::uint16_t slot = 0;
};

// 记录即"按 schema 顺序编码的一行"，编码与快照行一致：
//   BIGINT 8 字节；VECTOR u32 维度 + dim*4 字节；VARCHAR u32 长度 + 字节。
std::string encode_record(const std::vector<Value>& values,
                          const Schema& schema);
std::vector<Value> decode_record(std::string_view record,
                                 const Schema& schema);

// 在记录字节上做零拷贝读取。
class RecordView {
 public:
  RecordView(std::string_view record, const Schema& schema)
      : record_(record), schema_(&schema) {}

  std::int64_t bigint(std::size_t column) const;
  std::string_view varchar(std::size_t column) const;
  const char* vector_bytes(std::size_t column) const;
  Value value(std::size_t column) const;
  std::int64_t pk() const { return bigint(static_cast<std::size_t>(schema_->pk_index)); }

 private:
  std::pair<const char*, std::uint32_t> locate(std::size_t column) const;

  std::string_view record_;
  const Schema* schema_;
};

double l2_distance_bytes(const char* vector_bytes, std::uint32_t dim,
                         const std::vector<float>& query);
double l2_distance_vectors(const std::vector<float>& lhs,
                           const std::vector<float>& rhs);

}  // namespace mvdb::storage
