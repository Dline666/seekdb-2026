#pragma once

#include <filesystem>
#include <string>

#include "exec/catalog.h"

namespace mvdb::storage {

// 写表定义文件 catalog.meta（行数据在各表的堆文件里）。
bool save_catalog(const std::filesystem::path& data_dir, const Catalog& catalog,
                  std::string* error);

// 读表定义并打开各表堆文件、重建主键索引。文件缺失时视为空库。
bool load_catalog(const std::filesystem::path& data_dir, Catalog* catalog,
                  std::string* error);

}  // namespace mvdb::storage
