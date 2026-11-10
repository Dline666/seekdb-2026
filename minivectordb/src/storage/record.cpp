#include "storage/record.h"

#include <cmath>
#include <cstring>
#include <string>

#if defined(__x86_64__) || defined(_M_X64)
#define MVDB_HAVE_X86 1
#include <immintrin.h>
#else
#define MVDB_HAVE_X86 0
#endif

#include "common/error.h"
#include "storage/schema_codec.h"

namespace mvdb::storage {
namespace {

std::uint32_t column_size(const ColumnType& type) {
  switch (type.kind) {
    case TypeKind::kBigInt:
      return 8;
    case TypeKind::kVector:
      return 4 + type.size * 4;  // u32 维度前缀 + dim 个 float32
    case TypeKind::kVarchar:
      return 0;  // 变长，需读长度
  }
  return 0;
}

#if MVDB_HAVE_X86
bool avx2_available() {
  static const bool available = __builtin_cpu_supports("avx2") != 0;
  return available;
}

// 双精度累加，与标量版本精度一致；未对齐读取。
__attribute__((target("avx2"))) double l2_bytes_avx2(
    const char* vector_bytes, std::uint32_t dim, const float* query) {
  const float* values = reinterpret_cast<const float*>(vector_bytes);
  __m256d acc0 = _mm256_setzero_pd();
  __m256d acc1 = _mm256_setzero_pd();
  std::uint32_t i = 0;
  for (; i + 8 <= dim; i += 8) {
    __m128 const a0 = _mm_loadu_ps(values + i);
    __m128 const b0 = _mm_loadu_ps(query + i);
    __m256d const d0 = _mm256_cvtps_pd(_mm_sub_ps(a0, b0));
    acc0 = _mm256_add_pd(acc0, _mm256_mul_pd(d0, d0));

    __m128 const a1 = _mm_loadu_ps(values + i + 4);
    __m128 const b1 = _mm_loadu_ps(query + i + 4);
    __m256d const d1 = _mm256_cvtps_pd(_mm_sub_ps(a1, b1));
    acc1 = _mm256_add_pd(acc1, _mm256_mul_pd(d1, d1));
  }
  __m256d const sum = _mm256_add_pd(acc0, acc1);
  __m128d const lo = _mm256_castpd256_pd128(sum);
  __m128d const hi = _mm256_extractf128_pd(sum, 1);
  __m128d const pair = _mm_add_pd(lo, hi);
  double total = _mm_cvtsd_f64(pair) +
                 _mm_cvtsd_f64(_mm_unpackhi_pd(pair, pair));
  for (; i < dim; ++i) {
    double const diff = static_cast<double>(values[i]) -
                        static_cast<double>(query[i]);
    total += diff * diff;
  }
  return std::sqrt(total);
}
#endif

}  // namespace

std::string encode_record(const std::vector<Value>& values,
                          const Schema& schema) {
  std::size_t hint = 0;
  for (std::size_t i = 0; i < schema.columns.size(); ++i) {
    const ColumnType& type = schema.columns[i].type;
    switch (type.kind) {
      case TypeKind::kBigInt:
        hint += 8;
        break;
      case TypeKind::kVector:
        hint += 4 + static_cast<std::size_t>(type.size) * 4;
        break;
      case TypeKind::kVarchar:
        hint += 4 + std::get<std::string>(values[i]).size();
        break;
    }
  }
  Writer writer;
  writer.reserve(hint);
  for (std::size_t i = 0; i < schema.columns.size(); ++i) {
    encode_value(&writer, schema.columns[i].type, values[i]);
  }
  return writer.data();
}

std::vector<Value> decode_record(std::string_view record,
                                 const Schema& schema) {
  Reader reader(record);
  std::vector<Value> values;
  values.reserve(schema.columns.size());
  for (const Column& column : schema.columns) {
    values.push_back(decode_value(&reader, column.type));
  }
  reader.require_done();
  return values;
}

std::pair<const char*, std::uint32_t> RecordView::locate(
    std::size_t column) const {
  const char* cursor = record_.data();
  const char* const end = record_.data() + record_.size();
  for (std::size_t i = 0; i < schema_->columns.size(); ++i) {
    const ColumnType& type = schema_->columns[i].type;
    std::uint32_t size = column_size(type);
    if (type.kind == TypeKind::kVarchar) {
      if (cursor + 4 > end) throw SqlError("corrupt record: truncated varchar");
      std::uint32_t length = 0;
      std::memcpy(&length, cursor, 4);
      size = 4 + length;
    }
    if (cursor + size > end) throw SqlError("corrupt record: out of bounds");
    if (i == column) return {cursor, size};
    cursor += size;
  }
  throw SqlError("corrupt record: column out of range");
}

std::int64_t RecordView::bigint(std::size_t column) const {
  auto const [data, size] = locate(column);
  if (size != 8) throw SqlError("corrupt record: expected BIGINT");
  std::int64_t value = 0;
  std::memcpy(&value, data, 8);
  return value;
}

std::string_view RecordView::varchar(std::size_t column) const {
  auto const [data, size] = locate(column);
  if (size < 4) throw SqlError("corrupt record: expected VARCHAR");
  std::uint32_t length = 0;
  std::memcpy(&length, data, 4);
  return std::string_view(data + 4, length);
}

const char* RecordView::vector_bytes(std::size_t column) const {
  auto const [data, size] = locate(column);
  (void)size;
  return data + 4;  // 跳过 u32 维度前缀
}

Value RecordView::value(std::size_t column) const {
  if (column >= schema_->columns.size()) {
    throw SqlError("internal error: column out of range");
  }
  const Column& col = schema_->columns[column];
  switch (col.type.kind) {
    case TypeKind::kBigInt:
      return bigint(column);
    case TypeKind::kVarchar:
      return std::string(varchar(column));
    case TypeKind::kVector: {
      auto const [data, size] = locate(column);
      (void)size;
      Vector vector;
      vector.data.resize(col.type.size);
      std::memcpy(vector.data.data(), data + 4,
                  static_cast<std::size_t>(col.type.size) * 4);
      return vector;
    }
  }
  throw SqlError("internal error: unknown column type");
}

double l2_distance_bytes(const char* vector_bytes, std::uint32_t dim,
                         const std::vector<float>& query) {
#if MVDB_HAVE_X86
  if (dim >= 8 && avx2_available()) {
    return l2_bytes_avx2(vector_bytes, dim, query.data());
  }
#endif
  double sum = 0.0;
  for (std::uint32_t i = 0; i < dim; ++i) {
    float value = 0.0F;
    std::memcpy(&value, vector_bytes + static_cast<std::size_t>(i) * 4, 4);
    double const diff = static_cast<double>(value) - static_cast<double>(query[i]);
    sum += diff * diff;
  }
  return std::sqrt(sum);
}

double l2_distance_vectors(const std::vector<float>& lhs,
                           const std::vector<float>& rhs) {
#if MVDB_HAVE_X86
  if (lhs.size() >= 8 && avx2_available()) {
    return l2_bytes_avx2(reinterpret_cast<const char*>(lhs.data()),
                         static_cast<std::uint32_t>(lhs.size()), rhs.data());
  }
#endif
  double sum = 0.0;
  for (std::size_t i = 0; i < lhs.size(); ++i) {
    double const diff = static_cast<double>(lhs[i]) - static_cast<double>(rhs[i]);
    sum += diff * diff;
  }
  return std::sqrt(sum);
}

}  // namespace mvdb::storage
