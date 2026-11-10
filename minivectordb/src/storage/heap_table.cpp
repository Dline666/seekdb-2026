#include "storage/heap_table.h"

#include <cstring>
#include <string>
#include <utility>
#include <vector>

#include "common/error.h"
#include "storage/page.h"

namespace mvdb::storage {
namespace {

constexpr std::size_t kSpaceThreshold = 256;  // 页内剩余空间达到该值才作为候选

}  // namespace

HeapTable::HeapTable(Schema schema, std::uint32_t file_id, FileManager* files,
                     BufferPool* pool)
    : schema_(std::move(schema)),
      file_id_(file_id),
      files_(files),
      pool_(pool),
      page_count_(files->page_count(file_id)) {}

bool HeapTable::find_rid(std::int64_t pk, Rid* rid) const {
  auto const it = pk_index_.find(pk);
  if (it == pk_index_.end()) return false;
  *rid = it->second;
  return true;
}

bool HeapTable::read(std::int64_t pk, std::string* record) const {
  Rid rid;
  if (!find_rid(pk, &rid)) return false;
  PinnedPage page = pool_->fetch(file_id_, rid.page);
  *record = std::string(page_record(page.data(), rid.slot));
  return true;
}

bool HeapTable::try_place(std::uint32_t page_no, const std::string& record,
                          Rid* rid) {
  PinnedPage page = pool_->fetch(file_id_, page_no);
  std::uint16_t slot = 0;
  if (!page_insert(page.data(), record, &slot)) return false;
  page.mark_dirty();
  *rid = Rid{page_no, slot};
  return true;
}

Rid HeapTable::allocate(const std::string& record) {
  for (auto it = space_pages_.end(); it != space_pages_.begin();) {
    --it;
    std::uint32_t const page_no = *it;
    Rid rid;
    if (try_place(page_no, record, &rid)) return rid;
    space_pages_.erase(it);
  }

  std::uint32_t const page_no = page_count_;
  files_->ensure_pages(file_id_, page_no + 1);
  {
    PinnedPage page = pool_->fetch(file_id_, page_no);
    page_init(page.data());
    page.mark_dirty();
  }
  ++page_count_;

  Rid rid;
  if (!try_place(page_no, record, &rid)) {
    throw SqlError("internal error: record does not fit in a page");
  }
  space_pages_.push_back(page_no);
  return rid;
}

void HeapTable::insert_row(const std::vector<Value>& values) {
  std::string const record = encode_record(values, schema_);
  Rid const rid = allocate(record);
  auto const pk = std::get<std::int64_t>(values[static_cast<std::size_t>(schema_.pk_index)]);
  pk_index_.emplace(pk, rid);
}

void HeapTable::upsert_row(const std::vector<Value>& values) {
  auto const pk = std::get<std::int64_t>(values[static_cast<std::size_t>(schema_.pk_index)]);
  if (pk_index_.find(pk) == pk_index_.end()) {
    insert_row(values);
    return;
  }
  std::vector<std::pair<int, Value>> changes;
  changes.reserve(values.size());
  for (std::size_t i = 0; i < values.size(); ++i) {
    changes.emplace_back(static_cast<int>(i), values[i]);
  }
  update_row(pk, changes);
}

void HeapTable::update_row(std::int64_t pk,
                           const std::vector<std::pair<int, Value>>& changes) {
  Rid rid;
  if (!find_rid(pk, &rid)) return;

  std::string old_record;
  {
    PinnedPage page = pool_->fetch(file_id_, rid.page);
    old_record = std::string(page_record(page.data(), rid.slot));
  }

  std::vector<Value> values = decode_record(old_record, schema_);
  for (const auto& [column, value] : changes) {
    if (column < 0 || static_cast<std::size_t>(column) >= values.size()) {
      throw SqlError("internal error: bad column index in update");
    }
    values[static_cast<std::size_t>(column)] = value;
  }
  std::string const new_record = encode_record(values, schema_);

  if (new_record.size() == old_record.size()) {
    PinnedPage page = pool_->fetch(file_id_, rid.page);
    std::memcpy(page_record_mutable(page.data(), rid.slot), new_record.data(),
                new_record.size());
    page.mark_dirty();
    return;
  }

  Rid const new_rid = allocate(new_record);
  {
    PinnedPage page = pool_->fetch(file_id_, rid.page);
    page_delete(page.data(), rid.slot);
    page.mark_dirty();
  }
  pk_index_[pk] = new_rid;
}

void HeapTable::delete_row(std::int64_t pk) {
  Rid rid;
  if (!find_rid(pk, &rid)) return;
  {
    PinnedPage page = pool_->fetch(file_id_, rid.page);
    page_delete(page.data(), rid.slot);
    page.mark_dirty();
  }
  pk_index_.erase(pk);
}

void HeapTable::rebuild_index() {
  pk_index_.clear();
  space_pages_.clear();
  for (std::uint32_t page_no = 0; page_no < page_count_; ++page_no) {
    PinnedPage page = pool_->fetch(file_id_, page_no);
    std::uint16_t const count = page_slot_count(page.data());
    for (std::uint16_t slot = 0; slot < count; ++slot) {
      if (!page_slot_alive(page.data(), slot)) continue;
      std::string_view const record = page_record(page.data(), slot);
      RecordView view(record, schema_);
      pk_index_.emplace(view.pk(), Rid{page_no, slot});
    }
    if (page_free_bytes(page.data()) >= kSpaceThreshold) {
      space_pages_.push_back(page_no);
    }
  }
}

}  // namespace mvdb::storage
