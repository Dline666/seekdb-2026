#pragma once

#include <cstdint>
#include <filesystem>
#include <string>
#include <vector>

namespace mvdb::storage {

// 表堆文件的打开、整页读写与 fsync。单线程使用。
class FileManager {
 public:
  FileManager() = default;
  ~FileManager();

  FileManager(const FileManager&) = delete;
  FileManager& operator=(const FileManager&) = delete;

  // 打开或创建文件，返回小整数 file_id。失败抛 SqlError。
  std::uint32_t open_or_create(const std::filesystem::path& path);
  std::uint32_t page_count(std::uint32_t file_id) const;
  // 把文件扩展到至少 count 页（新增部分为 0）。
  void ensure_pages(std::uint32_t file_id, std::uint32_t count);
  // 清空文件（重建同名表时使用）。
  void truncate(std::uint32_t file_id);

  void read_page(std::uint32_t file_id, std::uint32_t page_no, char* data);
  void write_page(std::uint32_t file_id, std::uint32_t page_no,
                  const char* data);
  void fsync_file(std::uint32_t file_id);
  void fsync_all();
  void close_all();

 private:
  struct Entry {
    int fd = -1;
    std::filesystem::path path;
    std::uint32_t pages = 0;
  };
  Entry& at(std::uint32_t file_id);

  std::vector<Entry> files_;
};

}  // namespace mvdb::storage
