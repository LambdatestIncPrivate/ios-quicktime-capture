// decoder.h
#pragma once
#include <stdbool.h>
#include <unistd.h>

// Declaration of functions
void set_cancelled(bool state);
ssize_t net_recv_all(int soc, void *buf, size_t len);
int convert_to_mp4(const char *output_filename, const uint32_t port_number, const char *address);
