#include "storage/page.h"

#include <cstring>

namespace mvdb::storage {
namespace {

std::uint16_t load_u16(const char* p) {
  return static_cast<std::uint16_t>(
      static_cast<unsigned char>(p[0]) |
      (static_cast<std::uint16_t>(static_cast<unsigned char>(p[1])) << 8));
}

void store_u16(char* p, std::uint16_t value) {
  p[0] = static_cast<char>(value & 0xFFU);
  p[1] = static_cast<char>((value >> 8) & 0xFFU);
}

std::size_t slot_base(std::uint16_t slot) {
  return kPageHeaderSize + static_cast<std::size_t>(slot) * kSlotSize;
}

// 把活记录压到页尾，回收墓碑占用的空间。槽下标不变。
void compact(char* page) {
  std::uint16_t const count = page_slot_count(page);
  std::uint16_t bottom = static_cast<std::uint16_t>(kPageSize);
  for (std::uint16_t s = 0; s < count; ++s) {
    std::size_t const base = slot_base(s);
    std::uint16_t const size = load_u16(page + base + 2);
    if (size == 0) continue;
    std::uint16_t const old_offset = load_u16(page + base);
    bottom = static_cast<std::uint16_t>(bottom - size);
    if (bottom != old_offset) {
      std::memmove(page + bottom, page + old_offset, size);
    }
    store_u16(page + base, bottom);
  }
  store_u16(page + 4, bottom);
}

}  // namespace

void page_init(char* page) {
  std::memset(page, 0, kPageSize);
  store_u16(page + 0, 0);
  store_u16(page + 2, static_cast<std::uint16_t>(kPageHeaderSize));
  store_u16(page + 4, static_cast<std::uint16_t>(kPageSize));
}

std::uint16_t page_slot_count(const char* page) { return load_u16(page + 0); }

std::uint16_t page_record_size(const char* page, std::uint16_t slot) {
  if (slot >= page_slot_count(page)) return 0;
  return load_u16(page + slot_base(slot) + 2);
}

bool page_slot_alive(const char* page, std::uint16_t slot) {
  return page_record_size(page, slot) != 0;
}

std::string_view page_record(const char* page, std::uint16_t slot) {
  std::size_t const base = slot_base(slot);
  std::uint16_t const offset = load_u16(page + base);
  std::uint16_t const size = load_u16(page + base + 2);
  return std::string_view(page + offset, size);
}

char* page_record_mutable(char* page, std::uint16_t slot) {
  std::size_t const base = slot_base(slot);
  return page + load_u16(page + base);
}

std::size_t page_free_bytes(const char* page) {
  std::uint16_t const free_top = load_u16(page + 2);
  std::uint16_t const free_bottom = load_u16(page + 4);
  std::size_t const slot_area =
      free_bottom >= free_top ? free_bottom - free_top : 0;

  // 记录区里未被活槽引用的字节属于墓碑，可在压缩后复用。
  std::size_t record_area = 0;
  if (free_bottom < kPageSize) record_area = kPageSize - free_bottom;
  std::size_t live = 0;
  std::uint16_t const count = page_slot_count(page);
  for (std::uint16_t s = 0; s < count; ++s) {
    live += load_u16(page + slot_base(s) + 2);
  }
  std::size_t const dead = record_area > live ? record_area - live : 0;
  return slot_area + dead;
}

bool page_insert(char* page, std::string_view record, std::uint16_t* slot) {
  if (record.empty() || record.size() + kSlotSize > kPageSize) return false;

  std::uint16_t const count = page_slot_count(page);
  std::uint16_t const free_top = load_u16(page + 2);
  std::uint16_t free_bottom = load_u16(page + 4);
  std::size_t const need = record.size();

  auto try_place = [&]() -> bool {
    if (static_cast<std::size_t>(free_bottom) <
        static_cast<std::size_t>(free_top) + kSlotSize + need) {
      return false;
    }
    free_bottom = static_cast<std::uint16_t>(free_bottom - need);
    std::memcpy(page + free_bottom, record.data(), need);
    std::size_t const base = slot_base(count);
    store_u16(page + base, free_bottom);
    store_u16(page + base + 2, static_cast<std::uint16_t>(need));
    store_u16(page + 0, static_cast<std::uint16_t>(count + 1));
    store_u16(page + 2, static_cast<std::uint16_t>(free_top + kSlotSize));
    store_u16(page + 4, free_bottom);
    *slot = count;
    return true;
  };

  if (try_place()) return true;
  compact(page);
  free_bottom = load_u16(page + 4);
  return try_place();
}

bool page_delete(char* page, std::uint16_t slot) {
  if (slot >= page_slot_count(page)) return false;
  std::size_t const base = slot_base(slot);
  if (load_u16(page + base + 2) == 0) return false;
  store_u16(page + base + 2, 0);
  return true;
}

}  // namespace mvdb::storage
