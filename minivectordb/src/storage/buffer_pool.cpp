#include "storage/buffer_pool.h"

#include <cstddef>

#include "common/error.h"

namespace mvdb::storage {

PinnedPage::~PinnedPage() { release(); }

PinnedPage::PinnedPage(PinnedPage&& other) noexcept
    : pool_(other.pool_),
      file_id_(other.file_id_),
      page_no_(other.page_no_),
      data_(other.data_),
      dirty_(other.dirty_) {
  other.pool_ = nullptr;
  other.data_ = nullptr;
}

PinnedPage& PinnedPage::operator=(PinnedPage&& other) noexcept {
  if (this != &other) {
    release();
    pool_ = other.pool_;
    file_id_ = other.file_id_;
    page_no_ = other.page_no_;
    data_ = other.data_;
    dirty_ = other.dirty_;
    other.pool_ = nullptr;
    other.data_ = nullptr;
  }
  return *this;
}

void PinnedPage::release() {
  if (pool_ != nullptr) {
    pool_->unpin(file_id_, page_no_, dirty_);
    pool_ = nullptr;
    data_ = nullptr;
  }
}

BufferPool::BufferPool(FileManager* files, std::size_t frames)
    : files_(files), frames_(frames == 0 ? 1 : frames) {
  frames_.resize(frames == 0 ? 1 : frames);
  free_frames_.reserve(frames_.size());
  for (std::size_t i = 0; i < frames_.size(); ++i) {
    frames_[i].data.resize(kPageSize);
    free_frames_.push_back(static_cast<std::uint32_t>(i));
  }
}

BufferPool::Frame* BufferPool::find(std::uint64_t key) {
  auto const it = map_.find(key);
  if (it == map_.end()) return nullptr;
  return &frames_[it->second];
}

void BufferPool::evict_into(std::uint32_t frame_index) {
  Frame& frame = frames_[frame_index];
  if (frame.valid && frame.dirty) {
    files_->write_page(frame.file_id, frame.page_no, frame.data.data());
  }
  if (frame.valid) map_.erase(key_of(frame.file_id, frame.page_no));
  frame.valid = false;
  frame.dirty = false;
}

std::uint32_t BufferPool::acquire_frame() {
  if (!free_frames_.empty()) {
    std::uint32_t const index = free_frames_.back();
    free_frames_.pop_back();
    return index;
  }
  // 从 LRU 尾部找一个未钉住的页
  for (auto it = lru_.end(); it != lru_.begin();) {
    --it;
    Frame& frame = frames_[*it];
    if (frame.pins == 0) {
      std::uint32_t const index = *it;
      lru_.erase(it);
      evict_into(index);
      return index;
    }
  }
  throw SqlError("buffer pool exhausted: too many pages pinned");
}

PinnedPage BufferPool::fetch(std::uint32_t file_id, std::uint32_t page_no) {
  std::uint64_t const key = key_of(file_id, page_no);
  Frame* frame = find(key);
  if (frame != nullptr) {
    ++frame->pins;
    lru_.splice(lru_.begin(), lru_, frame->lru);
    return PinnedPage(this, file_id, page_no, frame->data.data());
  }

  std::uint32_t const index = acquire_frame();
  Frame& target = frames_[index];
  target.file_id = file_id;
  target.page_no = page_no;
  target.valid = true;
  target.dirty = false;
  target.pins = 1;
  files_->read_page(file_id, page_no, target.data.data());
  lru_.push_front(index);
  target.lru = lru_.begin();
  map_.emplace(key, index);
  return PinnedPage(this, file_id, page_no, target.data.data());
}

void BufferPool::unpin(std::uint32_t file_id, std::uint32_t page_no,
                       bool dirty) {
  Frame* frame = find(key_of(file_id, page_no));
  if (frame == nullptr) return;
  if (frame->pins > 0) --frame->pins;
  if (dirty) frame->dirty = true;
}

void BufferPool::drop_file(std::uint32_t file_id) {
  for (std::uint32_t index = 0; index < frames_.size(); ++index) {
    Frame& frame = frames_[index];
    if (!frame.valid || frame.file_id != file_id) continue;
    if (frame.pins != 0) {
      throw SqlError("internal error: dropping a pinned page");
    }
    map_.erase(key_of(frame.file_id, frame.page_no));
    lru_.erase(frame.lru);
    frame.valid = false;
    frame.dirty = false;
    free_frames_.push_back(index);
  }
}

void BufferPool::flush_all() {
  for (Frame& frame : frames_) {
    if (frame.valid && frame.dirty) {
      files_->write_page(frame.file_id, frame.page_no, frame.data.data());
      frame.dirty = false;
    }
  }
  files_->fsync_all();
}

}  // namespace mvdb::storage
