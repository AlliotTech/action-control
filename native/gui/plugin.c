#define _GNU_SOURCE
#include "gui.h"
#include "sha256.h"
#include <dlfcn.h>
#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <sys/syscall.h>
#include <unistd.h>
#include <sys/socket.h>
#include <netinet/in.h>
#include <arpa/inet.h>
#include <sys/time.h>

#ifndef AC_REAL_IMAGE_PLUGIN
#define AC_REAL_IMAGE_PLUGIN "/usr/lib/libgui_image_loader.so"
#endif

AcStatus ac_status;
unsigned char ac_qr_wifi[AC_QR_MAX*AC_QR_MAX];
unsigned char ac_qr_url[AC_QR_MAX*AC_QR_MAX];
int ac_qr_wifi_size,ac_qr_url_size;
_Atomic int ac_qr_ready;
static pthread_once_t real_once=PTHREAD_ONCE_INIT, attach_once=PTHREAD_ONCE_INIT;
static int (*real_create)(const char *,void **);
static void *real_library,*ew_context;
static const uint32_t *ew_update_cycle;
static int menu_mode;
static char state_dir[256];
static _Atomic int attached;

static void load_original(void) {
    real_library=dlopen(AC_REAL_IMAGE_PLUGIN,RTLD_NOW|RTLD_LOCAL|RTLD_NODELETE);
    if(real_library)real_create=(int (*)(const char *,void **))dlsym(real_library,"gui_image_loader_create");
    if(!real_create)fprintf(stderr,"action-control-gui: original image plugin unavailable: %s\n",dlerror());
}

static void user_event(void *unused,int32_t *changed) {
    (void)unused;
    atomic_fetch_add_explicit(&ac_status.callback_ticks,1,memory_order_relaxed);
    if(ew_update_cycle)atomic_store(&ac_status.ew_update_cycle,*ew_update_cycle);
    long tid=syscall(SYS_gettid);
    long expected=0;
    atomic_compare_exchange_strong(&ac_status.callback_tid,&expected,tid);
    if(expected && expected!=tid){atomic_store(&ac_status.error,4);return;}
    /* Read only after the framework has created its root, never wait in a factory. */
    void *root=NULL;
    memcpy(&root,ew_context,sizeof(root));
    atomic_store(&ac_status.root_ready,root!=NULL);
    if(root && menu_mode)ac_ui_tick(ew_context,changed);
}

/* Worker-thread only (blocking IO off the GUI thread). In native mode wlan0 is
   only ever the SoftAP interface; managed-mode STA is a separate workflow, so
   operstate "up" is a truthful proxy for "hotspot on" here. */
static int read_wlan0_on(void) {
    int fd=open("/sys/class/net/wlan0/operstate",O_RDONLY|O_CLOEXEC);
    if(fd<0)return 2;
    char buf[16]={0};ssize_t n=read(fd,buf,sizeof(buf)-1);close(fd);
    if(n<=0)return 2;
    return strncmp(buf,"up",2)==0?1:0;
}

/* Minimal one-shot HTTP POST to the local backend. Returns 1 on HTTP 200.
   Blocking with short timeouts; runs only in the worker thread, never the GUI. */
static int post_hotspot(const char *action) {
    int s=socket(AF_INET,SOCK_STREAM,0);
    if(s<0)return 0;
    struct timeval tv={.tv_sec=12};
    setsockopt(s,SOL_SOCKET,SO_SNDTIMEO,&tv,sizeof(tv));
    setsockopt(s,SOL_SOCKET,SO_RCVTIMEO,&tv,sizeof(tv));
    struct sockaddr_in addr={.sin_family=AF_INET,.sin_port=htons(8080)};
    addr.sin_addr.s_addr=htonl(INADDR_LOOPBACK);
    if(connect(s,(struct sockaddr *)&addr,sizeof(addr))<0){close(s);return 0;}
    char body[64];int blen=snprintf(body,sizeof(body),"{\"action\":\"%s\"}",action);
    char req[256];int rlen=snprintf(req,sizeof(req),
        "POST /api/native_hotspot HTTP/1.0\r\nHost: 127.0.0.1\r\n"
        "Content-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
        blen,body);
    if(write(s,req,(size_t)rlen)!=rlen){close(s);return 0;}
    char resp[64]={0};ssize_t got=read(s,resp,sizeof(resp)-1);close(s);
    return got>12 && memcmp(resp,"HTTP/1.",7)==0 && memcmp(resp+9,"200",3)==0;
}

/* One-shot HTTP GET to the backend; fills buf with the whole response and
   returns byte count on HTTP 200, else 0. Worker thread only. */
static int http_get(const char *path,char *buf,int cap) {
    int s=socket(AF_INET,SOCK_STREAM,0);
    if(s<0)return 0;
    struct timeval tv={.tv_sec=12};
    setsockopt(s,SOL_SOCKET,SO_SNDTIMEO,&tv,sizeof(tv));
    setsockopt(s,SOL_SOCKET,SO_RCVTIMEO,&tv,sizeof(tv));
    struct sockaddr_in addr={.sin_family=AF_INET,.sin_port=htons(8080)};
    addr.sin_addr.s_addr=htonl(INADDR_LOOPBACK);
    if(connect(s,(struct sockaddr *)&addr,sizeof(addr))<0){close(s);return 0;}
    char req[256];int rlen=snprintf(req,sizeof(req),
        "GET %s HTTP/1.0\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n",path);
    if(write(s,req,(size_t)rlen)!=rlen){close(s);return 0;}
    int total=0,got;
    while(total<cap-1 && (got=(int)read(s,buf+total,(size_t)(cap-1-total)))>0)total+=got;
    close(s);
    buf[total]=0;
    if(total<12 || memcmp(buf,"HTTP/1.",7) || memcmp(buf+9,"200",3))return 0;
    return total;
}

/* Extract the "matrix":["01..",..] rows into cells (stride AC_QR_MAX). Returns
   the square module count, or 0 if the payload is malformed or oversized. */
static int parse_qr(const char *body,unsigned char *cells) {
    const char *p=strstr(body,"\"matrix\":");
    if(!p)return 0;
    p+=9;
    int size=-1,y=0;
    while((p=strchr(p,'"'))!=NULL) {
        p++;
        const char *end=strchr(p,'"');
        if(!end)break;
        int len=(int)(end-p);
        if(size<0)size=len;
        if(len!=size || size<=0 || size>AC_QR_MAX || y>=AC_QR_MAX)return 0;
        for(int x=0;x<len;x++)cells[y*AC_QR_MAX+x]=(unsigned char)(p[x]=='1');
        y++;
        const char *q=end+1;
        while(*q==' '||*q==',')q++;
        p=end+1;
        if(*q==']')break;
    }
    return size>0 && y==size ? size : 0;
}

static int fetch_qr(const char *path,unsigned char *cells) {
    char buf[8192];
    if(!http_get(path,buf,sizeof(buf)))return 0;
    const char *body=strstr(buf,"\r\n\r\n");
    return parse_qr(body?body+4:buf,cells);
}

static void *report_worker(void *unused) {
    (void)unused;
    char target[320],temporary[320],control[320];
    snprintf(target,sizeof(target),"%s/status.json",state_dir);
    snprintf(temporary,sizeof(temporary),"%s/status.tmp",state_dir);
    snprintf(control,sizeof(control),"%s/command",state_dir);
    for(;;) {
        char buffer[1024];
        int n=snprintf(buffer,sizeof(buffer),"{\"pid\":%ld,\"attached\":%d,\"callback_ticks\":%lu,\"ew_update_cycle\":%u,\"callback_tid\":%ld,\"root_ready\":%d,\"panel_visible\":%d,\"menu_attached\":%d,\"clicks\":%d,\"entries\":%d,\"closes\":%d,\"menu_loads\":%d,\"native_items\":%d,\"width\":%d,\"height\":%d,\"control_found\":%d,\"touch_hits\":%d,\"command_result\":%d,\"error\":%d}\n",
            (long)getpid(),atomic_load(&attached),atomic_load(&ac_status.callback_ticks),atomic_load(&ac_status.ew_update_cycle),atomic_load(&ac_status.callback_tid),atomic_load(&ac_status.root_ready),atomic_load(&ac_status.panel_visible),atomic_load(&ac_status.menu_attached),atomic_load(&ac_status.clicks),atomic_load(&ac_status.entries),atomic_load(&ac_status.closes),atomic_load(&ac_status.menu_loads),atomic_load(&ac_status.native_items),atomic_load(&ac_status.root_width),atomic_load(&ac_status.root_height),atomic_load(&ac_status.control_found),atomic_load(&ac_status.touch_hits),atomic_load(&ac_status.command_result),atomic_load(&ac_status.error));
        int fd=open(temporary,O_WRONLY|O_CREAT|O_TRUNC|O_CLOEXEC|O_NOFOLLOW,0600);
        if(fd>=0) {
            ssize_t written=write(fd,buffer,(size_t)n);
            close(fd);
            if(written==n)rename(temporary,target);
        }
        if(atomic_load(&ac_ui_debug_ready)) {
            char path[320];snprintf(path,sizeof(path),"%s/tree.txt",state_dir);
            fd=open(path,O_WRONLY|O_CREAT|O_TRUNC|O_CLOEXEC|O_NOFOLLOW,0600);
            if(fd>=0){(void)write(fd,ac_ui_debug,strlen(ac_ui_debug));close(fd);}
            atomic_store(&ac_ui_debug_ready,0);
        }
        fd=atomic_load(&ac_status.command)?-1:open(control,O_RDONLY|O_CLOEXEC|O_NOFOLLOW);
        if(fd>=0) {
            char command[96]={0};ssize_t count=read(fd,command,sizeof(command)-1);close(fd);
            if(count>0) {
                int x1,y1,x2,y2,end=0;
                if(!strcmp(command,"panel\n"))atomic_store(&ac_status.command,1);
                else if(!strcmp(command,"close\n"))atomic_store(&ac_status.command,2);
                else if(!strcmp(command,"settings\n"))atomic_store(&ac_status.command,3);
                else if(!strcmp(command,"entry\n"))atomic_store(&ac_status.command,6);
                else if(!strcmp(command,"dump\n"))atomic_store(&ac_status.command,7);
                else if(sscanf(command,"tap %d %d%n",&x1,&y1,&end)==2 && !strcmp(command+end,"\n") && x1>=0 && y1>=0 && x1<2048 && y1<2048) {
                    atomic_store(&ac_status.point_from,(uint64_t)(uint32_t)x1|((uint64_t)(uint32_t)y1<<32));
                    atomic_store(&ac_status.point_to,atomic_load(&ac_status.point_from));
                    atomic_store(&ac_status.command,4);
                }
                else if(sscanf(command,"swipe %d %d %d %d%n",&x1,&y1,&x2,&y2,&end)==4 && !strcmp(command+end,"\n") && x1>=0 && y1>=0 && x2>=0 && y2>=0 && x1<2048 && y1<2048 && x2<2048 && y2<2048) {
                    atomic_store(&ac_status.point_from,(uint64_t)(uint32_t)x1|((uint64_t)(uint32_t)y1<<32));
                    atomic_store(&ac_status.point_to,(uint64_t)(uint32_t)x2|((uint64_t)(uint32_t)y2<<32));
                    atomic_store(&ac_status.command,5);
                }
                unlink(control);
            }
        }
        /* Service a hotspot toggle set by the GUI button, then refresh state.
           The GUI thread never blocks: it only sets hotspot_request. */
        int request=atomic_exchange(&ac_status.hotspot_request,0);
        if(request==1 || request==2) {
            atomic_store(&ac_status.hotspot_busy,1);
            post_hotspot(request==1?"start":"stop");
            atomic_store(&ac_status.hotspot_busy,0);
        }
        atomic_store(&ac_status.hotspot_on,read_wlan0_on());
        /* Fetch both QR matrices once the backend answers; retry each 500ms
           tick until both land, then publish for the GUI thread to draw. */
        if(!atomic_load(&ac_qr_ready)) {
            int w=fetch_qr("/api/hotspot_qr?kind=wifi",ac_qr_wifi);
            int u=fetch_qr("/api/hotspot_qr?kind=url",ac_qr_url);
            if(w>0 && u>0) {
                ac_qr_wifi_size=w;ac_qr_url_size=u;
                atomic_store(&ac_qr_ready,1);
            }
        }
        usleep(500000);
    }
    return NULL;
}

static void attach_extension(void) {
    const char *mode=getenv("ACTION_CONTROL_GUI_MODE");
    const char *directory=getenv("ACTION_CONTROL_GUI_STATE");
    if(!mode || (strcmp(mode,"probe") && strcmp(mode,"menu")))return;
    if(!directory || strncmp(directory,"/run/action-control-ui-",23) || strlen(directory)>=sizeof(state_dir))return;
    struct stat st;
    if(lstat(directory,&st) || !S_ISDIR(st.st_mode) || st.st_uid!=0 || (st.st_mode&0022))return;
    memcpy(state_dir,directory,strlen(directory)+1);
    char digest[65];
    if(ac_sha256_file("/proc/self/exe",digest) || strcmp(digest,"30eb933b1c2315984e150885124d277c13c1cef3194ba6c08cb3d4d1f6656db1")) {
        fprintf(stderr,"action-control-gui: unsupported rear GUI fingerprint; using original plugin\n");return;
    }
    void *(*get_service)(void)=(void *(*)(void))dlsym(RTLD_DEFAULT,"get_gui_service_handle");
    void (*register_event)(void *,AcUserEvent,void *,int32_t *)=(void (*)(void *,AcUserEvent,void *,int32_t *))dlsym(RTLD_DEFAULT,"EwGuiRegisterUserEvent");
    if(!get_service || !register_event)return;
    ew_update_cycle=(const uint32_t *)dlsym(RTLD_DEFAULT,"EwUpdateCycle");
    if(!ew_update_cycle)return;
    void *service=get_service();
    if(!service)return;
    memcpy(&ew_context,service,sizeof(ew_context));
    if(!ew_context)return;
    void *slots[3];memcpy(slots,(char *)ew_context+0x70,sizeof(slots));
    if(slots[0]||slots[1]||slots[2]) {
        fprintf(stderr,"action-control-gui: user event slot occupied; using original plugin\n");return;
    }
    menu_mode=!strcmp(mode,"menu");
    if(menu_mode && ac_ui_prepare()) {atomic_store(&ac_status.error,2);menu_mode=0;}
    pthread_t worker;
    if(pthread_create(&worker,NULL,report_worker,NULL))return;
    pthread_detach(worker);
    atomic_thread_fence(memory_order_release);
    register_event(ew_context,user_event,NULL,NULL);
    atomic_store(&attached,1);
    fprintf(stderr,"action-control-gui: registered %s callback\n",menu_mode?"menu":"probe");
}

__attribute__((visibility("default")))
int gui_image_loader_create(const char *parameters,void **out_interface) {
    pthread_once(&real_once,load_original);
    if(!real_create){if(out_interface)*out_interface=NULL;return -1001;}
    int result=real_create(parameters,out_interface);
    if(!result && out_interface && *out_interface)pthread_once(&attach_once,attach_extension);
    return result;
}
