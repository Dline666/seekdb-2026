#include "storage/wal.h"

#include <cstdio>
#include <cstring>
#include <string>
#include <string_view>
#include <utility>
#include <vector>

#include "common/error.h"
#include "exec/apply.h"
#include "exec/catalog.h"
#include "platform/fs_sync.h"
#include "storage/encoding.h"
#include "storage/schema_codec.h"

namespace mvdb::storage {
namespace {

constexpr char kMagic[8] = {'M', 'V', 'D', 'W', 'A', 'L', '0', '1'};
constexpr std::uint8_t kCommitType = 6;
constexpr std::uint32_t kMaxRecordSize = 64U * 1024U * 1024U;

void append_u32(std::string* out, std::uint32_t value) {
  for (int i = 0; i < 4; ++i) {
    out->push_back(static_cast<char>((value >> (8 * i)) & 0xFFU));
  }
}

std::uint32_t load_u32(const unsigned char* data) {
  std::uint32_t value = 0;
  for (int i = 0; i < 4; ++i) {
    value |= static_cast<std::uint32_t>(data[i]) << (8 * i);
  }
  return value;
}

std::string encode_payload(const WalRecord& record, const Catalog& catalog) {
  Writer writer;
  if (const auto* create = std::get_if<CreateTableRecord>(&record)) {
    writer.u8(1);
    encode_schema(&writer, create->schema);
  } else if (const auto* index = std::get_if<CreateIndexRecord>(&record)) {
    writer.u8(2);
    writer.bytes(index->table);
    writer.bytes(index->index);
  } else if (const auto* insert = std::get_if<InsertRecord>(&record)) {
    const HeapTable* table = catalog.find(insert->table);
    if (table == nullptr) {
      throw SqlError("internal error: WAL insert for unknown table '" +
                     insert->table + "'");
    }
    const Schema& schema = table->schema();
    writer.u8(3);
    writer.bytes(insert->table);
    writer.u32(static_cast<std::uint32_t>(insert->rows.size()));
    for (const std::vector<Value>& row : insert->rows) {
      if (row.size() != schema.columns.size()) {
        throw SqlError("internal error: row shape mismatch in WAL insert");
      }
      for (std::size_t i = 0; i < row.size(); ++i) {
        encode_value(&writer, schema.columns[i].type, row[i]);
      }
    }
  } else if (const auto* update = std::get_if<UpdateRecord>(&record)) {
    const HeapTable* table = catalog.find(update->table);
    if (table == nullptr) {
      throw SqlError("internal error: WAL update for unknown table '" +
                     update->table + "'");
    }
    const Schema& schema = table->schema();
    writer.u8(4);
    writer.bytes(update->table);
    writer.i64(update->pk);
    writer.u32(static_cast<std::uint32_t>(update->values.size()));
    for (const auto& [column, value] : update->values) {
      if (column < 0 ||
          static_cast<std::size_t>(column) >= schema.columns.size()) {
        throw SqlError("internal error: bad column index in WAL update");
      }
      writer.u32(static_cast<std::uint32_t>(column));
      encode_value(&writer, schema.columns[static_cast<std::size_t>(column)].type,
                   value);
    }
  } else {
    const auto& remove = std::get<DeleteRecord>(record);
    writer.u8(5);
    writer.bytes(remove.table);
    writer.i64(remove.pk);
  }
  return writer.data();
}

WalRecord decode_payload(std::uint8_t type, std::string_view payload,
                         const Catalog& catalog) {
  Reader reader(payload);
  switch (type) {
    case 1: {
      CreateTableRecord record;
      record.schema = decode_schema(&reader);
      reader.require_done();
      return record;
    }
    case 2: {
      CreateIndexRecord record;
      record.table = reader.bytes();
      record.index = reader.bytes();
      reader.require_done();
      return record;
    }
    case 3: {
      InsertRecord record;
      record.table = reader.bytes();
      const HeapTable* table = catalog.find(record.table);
      if (table == nullptr) {
        throw SqlError("corrupt WAL: insert into unknown table '" +
                       record.table + "'");
      }
      const Schema& schema = table->schema();
      std::uint32_t const count = reader.u32();
      record.rows.reserve(count);
      for (std::uint32_t r = 0; r < count; ++r) {
        std::vector<Value> row;
        row.reserve(schema.columns.size());
        for (const Column& column : schema.columns) {
          row.push_back(decode_value(&reader, column.type));
        }
        record.rows.push_back(std::move(row));
      }
      reader.require_done();
      return record;
    }
    case 4: {
      UpdateRecord record;
      record.table = reader.bytes();
      record.pk = reader.i64();
      const HeapTable* table = catalog.find(record.table);
      if (table == nullptr) {
        throw SqlError("corrupt WAL: update on unknown table '" +
                       record.table + "'");
      }
      const Schema& schema = table->schema();
      std::uint32_t const count = reader.u32();
      record.values.reserve(count);
      for (std::uint32_t i = 0; i < count; ++i) {
        std::uint32_t const column = reader.u32();
        if (column >= schema.columns.size()) {
          throw SqlError("corrupt WAL: column index out of range");
        }
        record.values.emplace_back(
            static_cast<int>(column),
            decode_value(&reader, schema.columns[column].type));
      }
      reader.require_done();
      return record;
    }
    case 5: {
      DeleteRecord record;
      record.table = reader.bytes();
      record.pk = reader.i64();
      reader.require_done();
      return record;
    }
    default:
      throw SqlError("corrupt WAL: unknown record type");
  }
}

}  // namespace

bool is_ddl(const WalRecord& record) {
  return std::holds_alternative<CreateTableRecord>(record) ||
         std::holds_alternative<CreateIndexRecord>(record);
}

WalFile::~WalFile() { close(); }

void WalFile::close() {
  if (file_ != nullptr) {
    std::fclose(file_);
    file_ = nullptr;
  }
}

bool WalFile::open(const std::filesystem::path& path, std::string* error) {
  close();
  std::string const name = path.string();
  std::FILE* file = std::fopen(name.c_str(), "rb+");
  if (file == nullptr) {
    file = std::fopen(name.c_str(), "wb+");
    if (file == nullptr) {
      *error = "cannot create " + name;
      return false;
    }
    bool ok = std::fwrite(kMagic, 1, sizeof kMagic, file) == sizeof kMagic;
    ok = std::fflush(file) == 0 && ok;
    ok = fsync_fd(file_descriptor(file)) == 0 && ok;
    if (!ok) {
      std::fclose(file);
      *error = "cannot initialize " + name;
      return false;
    }
    file_ = file;
    return true;
  }

  char magic[sizeof kMagic];
  if (std::fread(magic, 1, sizeof magic, file) != sizeof magic ||
      std::memcmp(magic, kMagic, sizeof kMagic) != 0) {
    std::fclose(file);
    *error = "invalid WAL header in " + name;
    return false;
  }
  file_ = file;
  return true;
}

bool WalFile::append_commit(const std::vector<WalRecord>& records,
                            const Catalog& catalog, std::string* error) {
  if (file_ == nullptr) {
    *error = "WAL is not open";
    return false;
  }
  std::string blob;
  try {
    for (const WalRecord& record : records) {
      std::string const payload = encode_payload(record, catalog);
      if (payload.empty() || payload.size() > kMaxRecordSize) {
        *error = "WAL record too large";
        return false;
      }
      append_u32(&blob, static_cast<std::uint32_t>(payload.size()));
      append_u32(&blob, crc32(payload));
      blob.append(payload);
    }
  } catch (const SqlError& exception) {
    *error = exception.what();
    return false;
  }

  std::string const marker(1, static_cast<char>(kCommitType));
  append_u32(&blob, static_cast<std::uint32_t>(marker.size()));
  append_u32(&blob, crc32(marker));
  blob.append(marker);

  if (std::fseek(file_, 0, SEEK_END) != 0 ||
      std::fwrite(blob.data(), 1, blob.size(), file_) != blob.size() ||
      std::fflush(file_) != 0 ||
      fsync_fd(file_descriptor(file_)) != 0) {
    *error = "WAL write failed";
    return false;
  }
  return true;
}

bool WalFile::replay(Catalog* catalog, bool* recovered, std::string* error) {
  *recovered = false;
  if (file_ == nullptr) {
    *error = "WAL is not open";
    return false;
  }
  if (std::fseek(file_, 0, SEEK_SET) != 0) {
    *error = "cannot seek WAL";
    return false;
  }

  char magic[sizeof kMagic];
  if (std::fread(magic, 1, sizeof magic, file_) != sizeof magic ||
      std::memcmp(magic, kMagic, sizeof kMagic) != 0) {
    *error = "invalid WAL header";
    return false;
  }

  std::vector<std::pair<std::uint8_t, std::string>> buffer;
  try {
    for (;;) {
      unsigned char length_bytes[4];
      if (std::fread(length_bytes, 1, 4, file_) != 4) break;
      std::uint32_t const length = load_u32(length_bytes);

      unsigned char crc_bytes[4];
      if (std::fread(crc_bytes, 1, 4, file_) != 4) break;
      std::uint32_t const expected = load_u32(crc_bytes);

      if (length == 0 || length > kMaxRecordSize) break;
      std::string payload(length, '\0');
      if (std::fread(payload.data(), 1, length, file_) != length) break;
      if (crc32(payload) != expected) break;

      auto const type = static_cast<std::uint8_t>(
          static_cast<unsigned char>(payload[0]));
      std::string_view const body(payload.data() + 1, payload.size() - 1);
      if (type == kCommitType) {
        for (const auto& [record_type, record_body] : buffer) {
          WalRecord record = decode_payload(record_type, record_body, *catalog);
          apply_record(record, catalog);
        }
        buffer.clear();
        *recovered = true;
      } else {
        buffer.emplace_back(type, std::string(body));
      }
    }
  } catch (const SqlError& exception) {
    *error = exception.what();
    return false;
  }
  return true;
}

bool WalFile::truncate(std::string* error) {
  if (file_ == nullptr) {
    *error = "WAL is not open";
    return false;
  }
  if (std::fflush(file_) != 0) {
    *error = "WAL flush failed";
    return false;
  }
  int const fd = file_descriptor(file_);
  if (truncate_fd(fd, sizeof kMagic) != 0) {
    *error = "cannot truncate WAL";
    return false;
  }
  if (fsync_fd(fd, false) != 0) {
    *error = "cannot sync WAL after truncate";
    return false;
  }
  std::fseek(file_, 0, SEEK_END);
  return true;
}

}  // namespace mvdb::storage
