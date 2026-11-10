#pragma once

#include <cstdint>
#include <cstdio>
#include <filesystem>

#ifdef _WIN32
#include <io.h>
#else
#include <fcntl.h>
#include <unistd.h>
#endif

namespace mvdb {

inline int file_descriptor(std::FILE* file) {
#ifdef _WIN32
  return ::_fileno(file);
#else
  return ::fileno(file);
#endif
}

// 把文件描述符刷到稳定存储。data_only 只在支持 fdatasync 的平台生效。
inline int fsync_fd(int fd, bool data_only = true) {
#ifdef _WIN32
  (void)data_only;
  return ::_commit(fd);
#else
  return data_only ? ::fdatasync(fd) : ::fsync(fd);
#endif
}

inline int truncate_fd(int fd, std::uint64_t size) {
#ifdef _WIN32
  return ::_chsize_s(fd, static_cast<__int64>(size));
#else
  return ::ftruncate(fd, static_cast<off_t>(size));
#endif
}

// 目录项落盘。Windows 不支持，跳过（正式评测在 Linux）。
inline void fsync_directory(const std::filesystem::path& path) {
#ifndef _WIN32
  int const fd = ::open(path.c_str(), O_RDONLY | O_DIRECTORY);
  if (fd >= 0) {
    ::fsync(fd);
    ::close(fd);
  }
#else
  (void)path;
#endif
}

}  // namespace mvdb
