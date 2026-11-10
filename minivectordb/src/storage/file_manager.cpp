#include "storage/file_manager.h"

#include <cstring>
#include <string>

#include "common/error.h"
#include "platform/fs_sync.h"
#include "storage/page.h"

#ifdef _WIN32
#include <fcntl.h>
#include <io.h>
#include <sys/stat.h>
#include <sys/types.h>
#else
#include <fcntl.h>
#include <sys/stat.h>
#include <unistd.h>
#endif

namespace mvdb::storage {
namespace {

int open_file(const std::string& path) {
#ifdef _WIN32
  return ::_open(path.c_str(), _O_RDWR | _O_CREAT | _O_BINARY, _S_IREAD | _S_IWRITE);
#else
  return ::open(path.c_str(), O_RDWR | O_CREAT, 0644);
#endif
}

void close_file(int fd) {
#ifdef _WIN32
  ::_close(fd);
#else
  ::close(fd);
#endif
}

int fd_offset(int fd, std::uint64_t offset) {
#ifdef _WIN32
  return ::_lseeki64(fd, static_cast<__int64>(offset), SEEK_SET) < 0 ? -1 : 0;
#else
  return ::lseek(fd, static_cast<off_t>(offset), SEEK_SET) < 0 ? -1 : 0;
#endif
}

void read_exact(int fd, char* data, std::size_t size, const char* what) {
  std::size_t done = 0;
  while (done < size) {
#ifdef _WIN32
    int const n = ::_read(fd, data + done, static_cast<unsigned int>(size - done));
#else
    ssize_t const n = ::read(fd, data + done, size - done);
#endif
    if (n <= 0) throw SqlError(std::string("read failed: ") + what);
    done += static_cast<std::size_t>(n);
  }
}

void write_exact(int fd, const char* data, std::size_t size, const char* what) {
  std::size_t done = 0;
  while (done < size) {
#ifdef _WIN32
    int const n = ::_write(fd, data + done, static_cast<unsigned int>(size - done));
#else
    ssize_t const n = ::write(fd, data + done, size - done);
#endif
    if (n <= 0) throw SqlError(std::string("write failed: ") + what);
    done += static_cast<std::size_t>(n);
  }
}

std::uint64_t file_size(int fd) {
#ifdef _WIN32
  __int64 const end = ::_lseeki64(fd, 0, SEEK_END);
  return end < 0 ? 0 : static_cast<std::uint64_t>(end);
#else
  off_t const end = ::lseek(fd, 0, SEEK_END);
  return end < 0 ? 0 : static_cast<std::uint64_t>(end);
#endif
}

}  // namespace

FileManager::~FileManager() { close_all(); }

std::uint32_t FileManager::open_or_create(const std::filesystem::path& path) {
  std::string const name = path.string();
  int const fd = open_file(name);
  if (fd < 0) throw SqlError("cannot open " + name);
  Entry entry;
  entry.fd = fd;
  entry.path = path;
  entry.pages = static_cast<std::uint32_t>(file_size(fd) / kPageSize);
  files_.push_back(entry);
  return static_cast<std::uint32_t>(files_.size() - 1);
}

FileManager::Entry& FileManager::at(std::uint32_t file_id) {
  if (file_id >= files_.size()) throw SqlError("internal error: bad file id");
  return files_[file_id];
}

std::uint32_t FileManager::page_count(std::uint32_t file_id) const {
  if (file_id >= files_.size()) throw SqlError("internal error: bad file id");
  return files_[file_id].pages;
}

void FileManager::ensure_pages(std::uint32_t file_id, std::uint32_t count) {
  Entry& entry = at(file_id);
  if (entry.pages >= count) return;
  std::uint64_t const bytes = static_cast<std::uint64_t>(count) * kPageSize;
#ifdef _WIN32
  if (::_chsize_s(entry.fd, static_cast<__int64>(bytes)) != 0) {
    throw SqlError("cannot extend " + entry.path.string());
  }
#else
  if (::ftruncate(entry.fd, static_cast<off_t>(bytes)) != 0) {
    throw SqlError("cannot extend " + entry.path.string());
  }
#endif
  entry.pages = count;
}

void FileManager::truncate(std::uint32_t file_id) {
  Entry& entry = at(file_id);
#ifdef _WIN32
  if (::_chsize_s(entry.fd, 0) != 0) {
    throw SqlError("cannot truncate " + entry.path.string());
  }
#else
  if (::ftruncate(entry.fd, 0) != 0) {
    throw SqlError("cannot truncate " + entry.path.string());
  }
#endif
  entry.pages = 0;
}

void FileManager::read_page(std::uint32_t file_id, std::uint32_t page_no,
                            char* data) {
  Entry& entry = at(file_id);
  if (page_no >= entry.pages) {
    throw SqlError("internal error: page out of range");
  }
  if (fd_offset(entry.fd, static_cast<std::uint64_t>(page_no) * kPageSize) != 0) {
    throw SqlError("seek failed: " + entry.path.string());
  }
  read_exact(entry.fd, data, kPageSize, entry.path.string().c_str());
}

void FileManager::write_page(std::uint32_t file_id, std::uint32_t page_no,
                             const char* data) {
  Entry& entry = at(file_id);
  if (page_no >= entry.pages) {
    throw SqlError("internal error: page out of range");
  }
  if (fd_offset(entry.fd, static_cast<std::uint64_t>(page_no) * kPageSize) != 0) {
    throw SqlError("seek failed: " + entry.path.string());
  }
  write_exact(entry.fd, data, kPageSize, entry.path.string().c_str());
}

void FileManager::fsync_file(std::uint32_t file_id) {
  Entry& entry = at(file_id);
  if (fsync_fd(entry.fd, false) != 0) {
    throw SqlError("fsync failed: " + entry.path.string());
  }
}

void FileManager::fsync_all() {
  for (Entry& entry : files_) {
    if (entry.fd >= 0 && fsync_fd(entry.fd, false) != 0) {
      throw SqlError("fsync failed: " + entry.path.string());
    }
  }
}

void FileManager::close_all() {
  for (Entry& entry : files_) {
    if (entry.fd >= 0) {
      close_file(entry.fd);
      entry.fd = -1;
    }
  }
  files_.clear();
}

}  // namespace mvdb::storage
