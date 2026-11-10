#pragma once

#include "exec/catalog.h"
#include "storage/wal.h"

namespace mvdb {

// 把一条已提交的 WAL 记录应用到堆存储。重放与提交后应用共用。
// 记录层面的幂等：插入按主键覆盖、更新写绝对值、删除按主键。
void apply_record(const storage::WalRecord& record, Catalog* catalog);

}  // namespace mvdb
