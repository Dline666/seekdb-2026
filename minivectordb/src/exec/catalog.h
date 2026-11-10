#pragma once

#include <cstdint>
#include <filesystem>
#include <memory>
#include <string>
#include <string_view>
#include <unordered_map>

#include "common/schema.h"
#include "storage/buffer_pool.h"
#include "storage/file_manager.h"
#include "storage/heap_table.h"

namespace mvdb {

// 表目录：内存中保存全部表定义，行数据在各自的堆文件里。
class Catalog {
 public:
  Catalog(storage::FileManager* files, storage::BufferPool* pool,
          std::filesystem::path data_dir);

  storage::HeapTable* find(std::string_view name);
  const storage::HeapTable* find(std::string_view name) const;

  // 打开（必要时创建）表；truncate_file 为真时清空堆文件。
  // 同名已存在时返回 nullptr。
  storage::HeapTable* attach(const Schema& schema, bool truncate_file);

  // 回滚建表：移出目录并丢弃该表缓存页。
  void erase(std::string_view name);

  std::unordered_map<std::string, std::unique_ptr<storage::HeapTable>>&
  tables() {
    return tables_;
  }
  const std::unordered_map<std::string, std::unique_ptr<storage::HeapTable>>&
  tables() const {
    return tables_;
  }

 private:
  storage::FileManager* files_;
  storage::BufferPool* pool_;
  std::filesystem::path data_dir_;
  std::unordered_map<std::string, std::unique_ptr<storage::HeapTable>> tables_;
};

std::filesystem::path heap_path(const std::filesystem::path& data_dir,
                                std::string_view table_name);

}  // namespace mvdb
