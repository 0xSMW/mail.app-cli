#ifndef MAIL_NATIVE_AE_H
#define MAIL_NATIVE_AE_H
// Returns an owned UTF-8 JSON result; caller frees it with free().
char *mail_ae_call(const char *json, int send_event, long timeout_ticks, int *attempted);
void mail_ae_clear_permission_cache(void);
#endif
