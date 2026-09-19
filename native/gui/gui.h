#ifndef AC_GUI_H
#define AC_GUI_H
#include <stdint.h>
#include <stdatomic.h>

/* Recovered only for the AC204 executable fingerprint checked by plugin.c. */
typedef struct { int32_t x1,y1,x2,y2; } AcRect;
typedef struct { int32_t x,y; } AcPoint;
typedef struct { void *object; void (*method)(void *,void *); } AcSlot;
_Static_assert(sizeof(AcRect)==16,"EW rectangle ABI");
_Static_assert(sizeof(AcPoint)==8,"EW point ABI");
_Static_assert(sizeof(AcSlot)==16,"EW slot ABI");
typedef void (*AcUserEvent)(void *,int32_t *);

typedef struct {
    _Atomic unsigned long callback_ticks;
    _Atomic uint32_t ew_update_cycle;
    _Atomic long callback_tid;
    _Atomic int root_ready;
    _Atomic int command;
    _Atomic int panel_visible;
    _Atomic int menu_attached;
    _Atomic int clicks;
    _Atomic int entries;
    _Atomic int closes;
    _Atomic int menu_loads;
    _Atomic int native_items;
    _Atomic int root_width;
    _Atomic int root_height;
    _Atomic int control_found;
    _Atomic int touch_hits;
    _Atomic int command_result;
    _Atomic uint64_t point_from;
    _Atomic uint64_t point_to;
    _Atomic int error;
    /* Hotspot toggle: request set by the GUI thread's button slot, serviced by
       the worker thread (HTTP to the backend). on/result written by the worker;
       the GUI tick only reads them to refresh the label. */
    _Atomic int hotspot_request; /* 0 none, 1 start, 2 stop */
    _Atomic int hotspot_on;      /* 0 off, 1 on, 2 unknown */
    _Atomic int hotspot_busy;    /* 1 while a request is in flight */
} AcStatus;

extern AcStatus ac_status;
extern char ac_ui_debug[65536];
extern _Atomic int ac_ui_debug_ready;

/* QR module matrices fetched once by the worker thread and read by the GUI
   thread when it builds the panel. Row-major, stride AC_QR_MAX; cell != 0 is a
   dark module. Published via ac_qr_ready (release/acquire); never rewritten. */
#define AC_QR_MAX 64
extern unsigned char ac_qr_wifi[AC_QR_MAX*AC_QR_MAX];
extern unsigned char ac_qr_url[AC_QR_MAX*AC_QR_MAX];
extern int ac_qr_wifi_size;
extern int ac_qr_url_size;
extern _Atomic int ac_qr_ready;
int ac_ui_prepare(void);
void ac_ui_tick(void *ew_context,int32_t *changed);
#endif
