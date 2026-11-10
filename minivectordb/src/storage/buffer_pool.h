#pragma once

#include <cstdint>
#include <list>
#include <unordered_map>
#include <vector>

#include "storage/file_manager.h"
#include "storage/page.h"

namespace mvdb::storage {

class BufferPool;

// 持有页的固定引用；析构时归还。页内指针只在生命周期内有效。
class PinnedPage {
 public:
  PinnedPage() = default;
  PinnedPage(BufferPool* pool, std::uint32_t file_id, std::uint32_t page_no,
             char* data)
      : pool_(pool), file_id_(file_id), page_no_(page_no), data_(data) {}

  ~PinnedPage();

  PinnedPage(PinnedPage&& other) noexcept;
  PinnedPage& operator=(PinnedPage&& other) noexcept;

  PinnedPage(const PinnedPage&) = delete;
  PinnedPage& operator=(const PinnedPage&) = delete;

  char* data() const { return data_; }
  void mark_dirty() { dirty_ = true; }
  bool valid() const { return pool_ != nullptr; }

  // 归还页；之后的 data() 不再有效。
  void release();

 private:
  BufferPool* pool_ = nullptr;
  std::uint32_t file_id_ = 0;
  std::uint32_t page_no_ = 0;
  char* data_ = nullptr;
  bool dirty_ = false;
};

// 固定帧数的页缓存。帧耗尽时按 LRU 淘汰未钉住的页，脏页先回写。
class BufferPool {
 public:
  BufferPool(FileManager* files, std::size_t frames);

  PinnedPage fetch(std::uint32_t file_id, std::uint32_t page_no);
  void unpin(std::uint32_t file_id, std::uint32_t page_no, bool dirty);

  // 回写全部脏页并 fsync 所有文件。
  void flush_all();

  // 丢弃某文件的全部缓存页（不写回），用于删除/重建表。
  void drop_file(std::uint32_t file_id);

  std::size_t frame_count() const { return frames_.size(); }

 private:
  struct Frame {
    std::uint32_t file_id = 0;
    std::uint32_t page_no = 0;
    bool valid = false;
    bool dirty = false;
    int pins = 0;
    std::list<std::uint32_t>::iterator lru;
    std::vector<char> data;
  };

  static std::uint64_t key_of(std::uint32_t file_id, std::uint32_t page_no) {
    return (static_cast<std::uint64_t>(file_id) << 32) | page_no;
  }

  Frame* find(std::uint64_t key);
  std::uint32_t acquire_frame();
  void evict_into(std::uint32_t frame_index);

  FileManager* files_;
  std::vector<Frame> frames_;
  std::vector<std::uint32_t> free_frames_;
  std::list<std::uint32_t> lru_;  // 最近使用在前
  std::unordered_map<std::uint64_t, std::uint32_t> map_;
};

}  // namespace mvdb::storage
