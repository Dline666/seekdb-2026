#pragma once

#include <cstdint>
#include <string>
#include <unordered_map>
#include <unordered_set>
#include <utility>
#include <vector>

#include "common/result.h"
#include "common/value.h"
#include "exec/catalog.h"
#include "sql/ast.h"
#include "storage/wal.h"

namespace mvdb::exec {

// 未提交事务的可见性覆盖层，按表名小写索引。
struct TableOverlay {
  std::unordered_map<std::int64_t, std::vector<std::pair<int, Value>>> updates;
  std::unordered_map<std::int64_t, std::vector<Value>> inserts;
  std::unordered_set<std::int64_t> deletes;
};

using OverlayMap = std::unordered_map<std::string, TableOverlay>;

// 执行一条语句。修改类语句只产出变更记录，不直接落页：
// 由 Engine 写 WAL 并 fsync 后再应用，保证页上只有已提交数据。
StatementResult execute(const sql::Statement& statement, Catalog& catalog,
                        const OverlayMap* overlay,
                        std::vector<storage::WalRecord>* changes);

}  // namespace mvdb::exec
