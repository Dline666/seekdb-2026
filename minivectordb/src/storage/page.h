#pragma once

#include <cstddef>
#include <cstdint>
#include <string_view>

namespace mvdb::storage {

// 8 KiB 槽页。
//   偏移 0: slot_count (u16)
//   偏移 2: free_top (u16)   —— 槽数组末端
//   偏移 4: free_bottom (u16) —— 记录区起点
//   偏移 6: 保留
// 槽从偏移 8 向下增长，每槽 u16 offset + u16 size；记录从页尾向前增长。
// size == 0 的槽是墓碑。
constexpr std::size_t kPageSize = 8192;
constexpr std::size_t kPageHeaderSize = 8;
constexpr std::size_t kSlotSize = 4;

void page_init(char* page);
std::uint16_t page_slot_count(const char* page);
bool page_slot_alive(const char* page, std::uint16_t slot);
std::string_view page_record(const char* page, std::uint16_t slot);
char* page_record_mutable(char* page, std::uint16_t slot);
std::uint16_t page_record_size(const char* page, std::uint16_t slot);

// 插入记录；页内空间不足（含压缩后）返回 false。
bool page_insert(char* page, std::string_view record, std::uint16_t* slot);
bool page_delete(char* page, std::uint16_t slot);

// 当前可插入字节数（含可压缩回收的墓碑空间）。
std::size_t page_free_bytes(const char* page);

}  // namespace mvdb::storage
