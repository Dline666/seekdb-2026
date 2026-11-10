#include "storage/schema_codec.h"

#include <string>
#include <variant>

#include "common/error.h"

namespace mvdb::storage {
namespace {

std::uint8_t kind_code(TypeKind kind) {
  switch (kind) {
    case TypeKind::kBigInt:
      return 0;
    case TypeKind::kVarchar:
      return 1;
    case TypeKind::kVector:
      return 2;
  }
  return 0;
}

TypeKind kind_from_code(std::uint8_t code) {
  switch (code) {
    case 0:
      return TypeKind::kBigInt;
    case 1:
      return TypeKind::kVarchar;
    case 2:
      return TypeKind::kVector;
    default:
      throw SqlError("corrupt data: unknown column type code");
  }
}

}  // namespace

void encode_schema(Writer* writer, const Schema& schema) {
  writer->bytes(schema.name);
  writer->u32(static_cast<std::uint32_t>(schema.columns.size()));
  for (const Column& column : schema.columns) {
    writer->bytes(column.name);
    writer->u8(kind_code(column.type.kind));
    writer->u32(column.type.size);
    writer->u8(column.primary_key ? 1U : 0U);
  }
  writer->i64(schema.pk_index);
  writer->i64(schema.vector_index);
  writer->bytes(schema.vector_index_name);
}

Schema decode_schema(Reader* reader) {
  Schema schema;
  schema.name = reader->bytes();
  std::uint32_t const count = reader->u32();
  schema.columns.reserve(count);
  for (std::uint32_t i = 0; i < count; ++i) {
    Column column;
    column.name = reader->bytes();
    column.type.kind = kind_from_code(reader->u8());
    column.type.size = reader->u32();
    column.primary_key = reader->u8() != 0;
    schema.columns.push_back(std::move(column));
  }
  schema.pk_index = static_cast<int>(reader->i64());
  schema.vector_index = static_cast<int>(reader->i64());
  schema.vector_index_name = reader->bytes();
  return schema;
}

void encode_value(Writer* writer, const ColumnType& type, const Value& value) {
  switch (type.kind) {
    case TypeKind::kBigInt: {
      const auto* integer = std::get_if<std::int64_t>(&value);
      if (integer == nullptr) throw SqlError("corrupt data: expected BIGINT");
      writer->i64(*integer);
      return;
    }
    case TypeKind::kVarchar: {
      const auto* text = std::get_if<std::string>(&value);
      if (text == nullptr) throw SqlError("corrupt data: expected VARCHAR");
      writer->bytes(*text);
      return;
    }
    case TypeKind::kVector: {
      const auto* vector = std::get_if<Vector>(&value);
      if (vector == nullptr) throw SqlError("corrupt data: expected VECTOR");
      writer->u32(static_cast<std::uint32_t>(vector->data.size()));
      for (float const element : vector->data) writer->f32(element);
      return;
    }
  }
}

Value decode_value(Reader* reader, const ColumnType& type) {
  switch (type.kind) {
    case TypeKind::kBigInt:
      return reader->i64();
    case TypeKind::kVarchar:
      return reader->bytes();
    case TypeKind::kVector: {
      std::uint32_t const dimension = reader->u32();
      if (dimension != type.size) {
        throw SqlError("corrupt data: vector dimension mismatch");
      }
      Vector vector;
      vector.data.reserve(dimension);
      for (std::uint32_t i = 0; i < dimension; ++i) {
        vector.data.push_back(reader->f32());
      }
      return vector;
    }
  }
  throw SqlError("corrupt data: unknown column type");
}

std::uint64_t schema_hash(const Schema& schema) {
  Writer writer;
  encode_schema(&writer, schema);
  std::uint64_t hash = 1469598103934665603ULL;
  for (char const c : writer.data()) {
    hash ^= static_cast<std::uint8_t>(static_cast<unsigned char>(c));
    hash *= 1099511628211ULL;
  }
  return hash;
}

}  // namespace mvdb::storage
