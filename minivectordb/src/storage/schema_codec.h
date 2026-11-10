#pragma once

#include <cstdint>

#include "common/value.h"
#include "exec/catalog.h"
#include "storage/encoding.h"

namespace mvdb::storage {

// 表定义与值在快照、WAL 中使用同一套编码。
void encode_schema(Writer* writer, const Schema& schema);
Schema decode_schema(Reader* reader);

void encode_value(Writer* writer, const ColumnType& type, const Value& value);
Value decode_value(Reader* reader, const ColumnType& type);

std::uint64_t schema_hash(const Schema& schema);

}  // namespace mvdb::storage
