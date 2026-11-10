#include "exec/catalog.h"

#include <string>
#include <utility>

#include "common/error.h"

namespace mvdb {

std::filesystem::path heap_path(const std::filesystem::path& data_dir,
                                std::string_view table_name) {
  return data_dir / (lowercase(table_name) + ".heap");
}

Catalog::Catalog(storage::FileManager* files, storage::BufferPool* pool,
                 std::filesystem::path data_dir)
    : files_(files), pool_(pool), data_dir_(std::move(data_dir)) {}

storage::HeapTable* Catalog::find(std::string_view name) {
  auto const it = tables_.find(lowercase(name));
  return it == tables_.end() ? nullptr : it->second.get();
}

const storage::HeapTable* Catalog::find(std::string_view name) const {
  auto const it = tables_.find(lowercase(name));
  return it == tables_.end() ? nullptr : it->second.get();
}

storage::HeapTable* Catalog::attach(const Schema& schema, bool truncate_file) {
  std::string const key = lowercase(schema.name);
  if (tables_.count(key) != 0) return nullptr;

  std::uint32_t const file_id =
      files_->open_or_create(heap_path(data_dir_, schema.name));
  if (truncate_file) {
    pool_->drop_file(file_id);
    files_->truncate(file_id);
  }
  auto table = std::make_unique<storage::HeapTable>(schema, file_id, files_,
                                                    pool_);
  storage::HeapTable* raw = table.get();
  tables_.emplace(key, std::move(table));
  return raw;
}

void Catalog::erase(std::string_view name) {
  std::string const key = lowercase(name);
  auto const it = tables_.find(key);
  if (it == tables_.end()) return;
  pool_->drop_file(it->second->file_id());
  tables_.erase(it);
}

}  // namespace mvdb
