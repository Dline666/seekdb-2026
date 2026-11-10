#include "exec/apply.h"

#include <string>
#include <variant>
#include <vector>

#include "common/error.h"

namespace mvdb {

void apply_record(const storage::WalRecord& record, Catalog* catalog) {
  if (const auto* create = std::get_if<storage::CreateTableRecord>(&record)) {
    if (catalog->find(create->schema.name) != nullptr) return;  // 已存在则跳过
    catalog->attach(create->schema, true);
    return;
  }
  if (const auto* index = std::get_if<storage::CreateIndexRecord>(&record)) {
    storage::HeapTable* table = catalog->find(index->table);
    if (table != nullptr && table->schema().vector_index_name.empty()) {
      table->mutable_schema().vector_index_name = index->index;
    }
    return;
  }
  if (const auto* insert = std::get_if<storage::InsertRecord>(&record)) {
    storage::HeapTable* table = catalog->find(insert->table);
    if (table == nullptr) {
      throw SqlError("WAL replay: insert into unknown table '" + insert->table +
                     "'");
    }
    for (const std::vector<Value>& values : insert->rows) {
      table->upsert_row(values);
    }
    return;
  }
  if (const auto* update = std::get_if<storage::UpdateRecord>(&record)) {
    storage::HeapTable* table = catalog->find(update->table);
    if (table != nullptr) table->update_row(update->pk, update->values);
    return;
  }
  const auto& remove = std::get<storage::DeleteRecord>(record);
  storage::HeapTable* table = catalog->find(remove.table);
  if (table != nullptr) table->delete_row(remove.pk);
}

}  // namespace mvdb
