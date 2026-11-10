#pragma once

#include <cstdint>
#include <cstdio>
#include <filesystem>
#include <string>
#include <utility>
#include <variant>
#include <vector>

#include "common/schema.h"
#include "common/value.h"

namespace mvdb {

class Catalog;  // 前向声明，仅供 append_commit 编码时查询表结构

namespace storage {

// WAL 记录描述"提交后应产生的状态"，重放具备幂等性。
struct CreateTableRecord {
  Schema schema;
};

struct CreateIndexRecord {
  std::string table;
  std::string index;
};

struct InsertRecord {
  std::string table;
  std::vector<std::vector<Value>> rows;
};

struct UpdateRecord {
  std::string table;
  std::int64_t pk = 0;
  std::vector<std::pair<int, Value>> values;  // 列下标 + 绝对值
};

struct DeleteRecord {
  std::string table;
  std::int64_t pk = 0;
};

using WalRecord = std::variant<CreateTableRecord, CreateIndexRecord,
                               InsertRecord, UpdateRecord, DeleteRecord>;

bool is_ddl(const WalRecord& record);

// 追加式预写日志。一次提交写入若干记录和一条提交标记，然后 fsync。
class WalFile {
 public:
  WalFile() = default;
  ~WalFile();

  WalFile(const WalFile&) = delete;
  WalFile& operator=(const WalFile&) = delete;

  bool open(const std::filesystem::path& path, std::string* error);
  bool append_commit(const std::vector<WalRecord>& records,
                     const Catalog& catalog, std::string* error);
  // 从文件头重放；只应用带提交标记的记录组。*recovered 表示是否有内容被应用。
  bool replay(Catalog* catalog, bool* recovered, std::string* error);
  // 只保留文件头，用于检查点之后清理。
  bool truncate(std::string* error);
  void close();

 private:
  std::FILE* file_ = nullptr;
};

}  // namespace storage
}  // namespace mvdb
