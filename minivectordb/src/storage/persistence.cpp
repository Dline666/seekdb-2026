#include "storage/persistence.h"

#include <algorithm>
#include <cstdio>
#include <filesystem>
#include <string>
#include <string_view>
#include <system_error>
#include <utility>
#include <vector>

#include "common/error.h"
#include "platform/fs_sync.h"
#include "storage/encoding.h"
#include "storage/schema_codec.h"

namespace mvdb::storage {
namespace {

constexpr char kCatalogMagic[8] = {'M', 'V', 'D', 'B', 'C', 'A', 'T', '2'};

bool read_file(const std::filesystem::path& path, std::string* out,
               std::string* error) {
  std::string const name = path.string();
  std::FILE* file = std::fopen(name.c_str(), "rb");
  if (file == nullptr) {
    *error = "cannot open " + name;
    return false;
  }
  std::fseek(file, 0, SEEK_END);
  long const size = std::ftell(file);
  std::fseek(file, 0, SEEK_SET);
  if (size < 0) {
    std::fclose(file);
    *error = "cannot size " + name;
    return false;
  }
  out->resize(static_cast<std::size_t>(size));
  bool ok = size == 0 || std::fread(out->data(), 1, out->size(), file) == out->size();
  std::fclose(file);
  if (!ok) {
    *error = "read failed: " + name;
    return false;
  }
  return true;
}

bool write_file(const std::filesystem::path& path, std::string_view data,
                std::string* error) {
  std::string const name = path.string();
  std::FILE* file = std::fopen(name.c_str(), "wb");
  if (file == nullptr) {
    *error = "cannot open " + name;
    return false;
  }
  bool ok = std::fwrite(data.data(), 1, data.size(), file) == data.size();
  ok = std::fflush(file) == 0 && ok;
  ok = fsync_fd(file_descriptor(file)) == 0 && ok;
  ok = std::fclose(file) == 0 && ok;
  if (!ok) *error = "write failed: " + name;
  return ok;
}

bool write_atomic(const std::filesystem::path& path, std::string_view data,
                  std::string* error) {
  std::filesystem::path temporary = path;
  temporary += ".tmp";
  if (!write_file(temporary, data, error)) return false;
  std::error_code ec;
  std::filesystem::rename(temporary, path, ec);
  if (ec) {
    *error = "rename failed for " + path.string() + ": " + ec.message();
    return false;
  }
  return true;
}

std::string encode_catalog(const Catalog& catalog) {
  Writer writer;
  writer.write_raw(std::string_view(kCatalogMagic, sizeof kCatalogMagic));

  std::vector<std::string> keys;
  keys.reserve(catalog.tables().size());
  for (const auto& [key, table] : catalog.tables()) keys.push_back(key);
  std::sort(keys.begin(), keys.end());

  writer.u32(static_cast<std::uint32_t>(keys.size()));
  for (const std::string& key : keys) {
    encode_schema(&writer, catalog.tables().at(key)->schema());
  }
  return writer.data();
}

}  // namespace

bool save_catalog(const std::filesystem::path& data_dir, const Catalog& catalog,
                  std::string* error) {
  if (!write_atomic(data_dir / "catalog.meta", encode_catalog(catalog), error)) {
    return false;
  }
  fsync_directory(data_dir);
  return true;
}

bool load_catalog(const std::filesystem::path& data_dir, Catalog* catalog,
                  std::string* error) {
  std::filesystem::path const meta_path = data_dir / "catalog.meta";
  std::error_code ec;
  if (!std::filesystem::exists(meta_path, ec)) {
    if (ec) {
      *error = "cannot inspect data directory: " + ec.message();
      return false;
    }
    return true;
  }

  std::string bytes;
  if (!read_file(meta_path, &bytes, error)) return false;

  std::vector<Schema> schemas;
  try {
    Reader reader(bytes);
    std::string_view const magic = reader.read_raw(sizeof kCatalogMagic);
    if (magic != std::string_view(kCatalogMagic, sizeof kCatalogMagic)) {
      *error = "invalid catalog file";
      return false;
    }
    std::uint32_t const count = reader.u32();
    schemas.reserve(count);
    for (std::uint32_t i = 0; i < count; ++i) {
      schemas.push_back(decode_schema(&reader));
    }
    reader.require_done();
  } catch (const SqlError& exception) {
    *error = std::string("catalog: ") + exception.what();
    return false;
  }

  for (const Schema& schema : schemas) {
    HeapTable* table = catalog->attach(schema, false);
    if (table == nullptr) continue;
    table->rebuild_index();
  }
  return true;
}

}  // namespace mvdb::storage
