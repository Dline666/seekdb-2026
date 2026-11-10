#pragma once

#include <cstdint>
#include <string>
#include <string_view>
#include <unordered_map>
#include <utility>
#include <vector>

#include "common/schema.h"
#include "common/value.h"
#include "storage/buffer_pool.h"
#include "storage/file_manager.h"
#include "storage/record.h"

namespace mvdb::storage {

// 一个表的堆存储：8 KiB 槽页 + 主键索引（内存哈希）。
// 记录在页内按 schema 顺序编码，RID = (页号, 槽号)。
class HeapTable {
 public:
  HeapTable(Schema schema, std::uint32_t file_id, FileManager* files,
            BufferPool* pool);

  const Schema& schema() const { return schema_; }
  Schema& mutable_schema() { return schema_; }
  std::uint32_t file_id() const { return file_id_; }
  std::uint64_t live_rows() const { return pk_index_.size(); }
  std::uint32_t page_count() const { return page_count_; }
  const std::unordered_map<std::int64_t, Rid>& pk_index() const {
    return pk_index_;
  }

  // 顺序扫描。回调期间页处于钉住状态，记录视图只在回调内有效。
  template <typename Fn>
  void scan(Fn&& fn) {
    for (std::uint32_t page_no = 0; page_no < page_count_; ++page_no) {
      PinnedPage page = pool_->fetch(file_id_, page_no);
      std::uint16_t const count = page_slot_count(page.data());
      for (std::uint16_t slot = 0; slot < count; ++slot) {
        if (!page_slot_alive(page.data(), slot)) continue;
        fn(Rid{page_no, slot}, page_record(page.data(), slot));
      }
    }
  }

  bool find_rid(std::int64_t pk, Rid* rid) const;
  bool read(std::int64_t pk, std::string* record) const;

  void insert_row(const std::vector<Value>& values);
  void upsert_row(const std::vector<Value>& values);
  void update_row(std::int64_t pk,
                  const std::vector<std::pair<int, Value>>& changes);
  void delete_row(std::int64_t pk);

  // 启动时扫描全部页重建主键索引与空闲页提示。
  void rebuild_index();

 private:
  Rid allocate(const std::string& record);
  bool try_place(std::uint32_t page_no, const std::string& record, Rid* rid);

  Schema schema_;
  std::uint32_t file_id_;
  FileManager* files_;
  BufferPool* pool_;
  std::uint32_t page_count_ = 0;
  std::unordered_map<std::int64_t, Rid> pk_index_;
  std::vector<std::uint32_t> space_pages_;
};

}  // namespace mvdb::storage
