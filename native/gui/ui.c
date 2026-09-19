#define _GNU_SOURCE
#include "gui.h"
#include <dlfcn.h>
#include <stddef.h>
#include <stdio.h>
#include <string.h>

/* Private EW fields below are valid ONLY for plugin.c's exact rear-GUI hash. */
enum {
    VIEW_PARENT=0x30, VIEW_BOUNDS=0x50,
    SETTINGS_LIST=0x13f8, SETTINGS_ITEM=0x1568, SETTINGS_LOAD_SLOT=0x1770,
    SETTINGS_INDEX=0x1ab4, SETTINGS_NATIVE_COUNT=0x2860,
    LIST_COUNT=0x6b8, ITEM_REFRESH_SLOT=0x1a0
};

static struct {
    void *group_class,*rectangle_class,*text_class,*item_class,*touch_class;
    void *settings_class,*control_class,*font_class,*font_resource,*font_zh_resource,*wrapper_class,*app_class,*liveview_class;
    void *(*new_object)(void *,void *);
    void *(*cast)(void *,void *);
    void *(*new_string)(const char *,int32_t);
    void *(*load_resource)(void *,void *);
    void (*lock)(void *);
    void (*unlock)(void *);
    uint32_t (*ticks)(void);
    void (*bounds)(void *,AcRect);
    void (*add)(void *,void *,int32_t);
    void (*remove)(void *,void *);
    void *(*next_view)(void *,void *,uint32_t);
    void (*embedded)(void *,int32_t);
    void (*text_string)(void *,void *);
    void (*text_font)(void *,void *);
    void (*text_color)(void *,uint32_t);
    void (*text_alignment)(void *,uint32_t);
    void (*rectangle_color)(void *,uint32_t);
    void (*item_string)(void *,void *);
    void (*item_heading)(void *,void *);
    void (*item_foot)(void *,void *);
    void (*item_icon)(void *,void *);
    void (*item_font)(void *,void *);
    void (*item_prior_font)(void *,void *);
    void (*item_options)(void *,uint32_t);
    void (*item_state)(void *,uint32_t);
    void (*item_disable)(void *,int32_t);
    void (*item_count)(void *,int32_t);
    void (*item_number)(void *,int32_t);
    void (*item_press)(void *,AcSlot);
    void (*item_disabled_press)(void *,AcSlot);
    void (*list_count)(void *,int32_t);
    void (*list_refresh)(void *,void *);
    void (*list_scroll)(void *,int32_t);
    void *(*list_item)(void *,int32_t);
    void (*native_load)(void *,void *);
    void (*open_settings)(void *,void *);
    int32_t (*touch_hit)(void *,int32_t,int32_t,AcPoint);
    int32_t (*touch_move)(void *,int32_t,AcPoint);
    void (*touch_point[4])(void *,AcPoint);
} ew;

static void *settings,*panel,*counter_text,*root_object;
static AcSlot original_load;
static int open_pending,close_pending,dump_pending;
static int shown_hotspot=-1;
static uint32_t last_scan;
static struct {int active;uint32_t start,duration;AcPoint from,to;} gesture;
char ac_ui_debug[65536];
_Atomic int ac_ui_debug_ready;
static size_t debug_used;

static void *pointer_at(void *object,size_t offset) {
    void *value;memcpy(&value,(char *)object+offset,sizeof(value));return value;
}
static int32_t int_at(void *object,size_t offset) {
    int32_t value;memcpy(&value,(char *)object+offset,sizeof(value));return value;
}
static AcRect bounds_of(void *object) {
    AcRect value;memcpy(&value,(char *)object+VIEW_BOUNDS,sizeof(value));return value;
}
static void *string(const char *text) {return ew.new_string(text,(int32_t)strlen(text));}
static const char *class_name(void *object) {
    return object?(const char *)pointer_at(pointer_at(object,0),8):"null";
}

static void dump_group(void *group,int depth,int *budget) {
    if(depth>18 || --*budget<0 || debug_used>sizeof(ac_ui_debug)-512)return;
    AcRect r=bounds_of(group);
    int n=snprintf(ac_ui_debug+debug_used,sizeof(ac_ui_debug)-debug_used,
        "%*s%p %s flags=%x bounds=%d,%d,%d,%d\n",depth*2,"",group,class_name(group),
        (unsigned)int_at(group,0x40),r.x1,r.y1,r.x2,r.y2);
    debug_used+=(size_t)n;
    void *child=NULL;
    while((child=ew.next_view(group,child,0)) && *budget>0) {
        if(ew.cast(child,ew.group_class))dump_group(child,depth+1,budget);
        else --*budget;
    }
}
static void dump_tree(void) {
    if(atomic_load(&ac_ui_debug_ready))return;
    void *hit=pointer_at(root_object,0xb8);
    debug_used=(size_t)snprintf(ac_ui_debug,sizeof(ac_ui_debug),"touch target: %p %s; panel: %p\n",hit,class_name(hit),panel);
    int budget=2048;dump_group(root_object,0,&budget);
    atomic_store(&ac_ui_debug_ready,1);
    dump_pending=0;
}

int ac_ui_prepare(void) {
#define RESOLVE(field,name) do {void *symbol=dlsym(RTLD_DEFAULT,name); if(!symbol)return -1; memcpy(&ew.field,&symbol,sizeof(symbol));} while(0)
    RESOLVE(group_class,"__vmt_CoreGroup");
    RESOLVE(rectangle_class,"__vmt_ViewsRectangle");
    RESOLVE(text_class,"__vmt_ViewsText");
    RESOLVE(item_class,"__vmt_WidgetsListItem");
    RESOLVE(touch_class,"__vmt_CoreSimpleTouchHandler");
    RESOLVE(settings_class,"__vmt_CameraCtrlCameraSettingPage");
    RESOLVE(control_class,"__vmt_CameraCtrlCameraCtrlPage");
    RESOLVE(wrapper_class,"__vmt_TestTestApp");
    RESOLVE(app_class,"__vmt_OsmoGuiOsmoApp");
    RESOLVE(liveview_class,"__vmt_OsmoGuiLiveviewLayer");
    RESOLVE(font_class,"__vmt_ResourcesFont");
    RESOLVE(font_resource,"FontsSize32Default");
    RESOLVE(font_zh_resource,"FontsSize32ZH");
    RESOLVE(new_object,"EwNewObjectIndirect");
    RESOLVE(cast,"EwCastObject");
    RESOLVE(new_string,"EwNewStringUtf8");
    RESOLVE(load_resource,"EwLoadResource");
    RESOLVE(lock,"EwLockObject");
    RESOLVE(unlock,"EwUnlockObject");
    RESOLVE(ticks,"EwGetTicks");
    RESOLVE(bounds,"CoreRectView__OnSetBounds");
    RESOLVE(add,"CoreGroup__Add");
    RESOLVE(remove,"CoreGroup__Remove");
    RESOLVE(next_view,"CoreGroup_FindNextView");
    RESOLVE(embedded,"CoreGroup_OnSetEmbedded");
    RESOLVE(text_string,"ViewsText_OnSetString");
    RESOLVE(text_font,"ViewsText_OnSetFont");
    RESOLVE(text_color,"ViewsText_OnSetColor");
    RESOLVE(text_alignment,"ViewsText_OnSetAlignment");
    RESOLVE(rectangle_color,"ViewsRectangle_OnSetColor");
    RESOLVE(item_string,"WidgetsListItem_OnSetMainString");
    RESOLVE(item_heading,"WidgetsListItem_OnSetHeadingString");
    RESOLVE(item_foot,"WidgetsListItem_OnSetFootFontString");
    RESOLVE(item_icon,"WidgetsListItem_OnSetIconBitmap");
    RESOLVE(item_font,"WidgetsListItem_OnSetAddtionalFont");
    RESOLVE(item_prior_font,"WidgetsListItem_OnSetPriorFont");
    RESOLVE(item_options,"WidgetsListItem_OnSetP2_Option");
    RESOLVE(item_state,"WidgetsListItem_OnSetP3_State");
    RESOLVE(item_disable,"WidgetsListItem_OnSetDisable");
    RESOLVE(item_count,"WidgetsListItem_OnSetItemCount");
    RESOLVE(item_number,"WidgetsListItem_OnSetItemNumber");
    RESOLVE(item_press,"WidgetsListItem_OnSetOnPressSlot");
    RESOLVE(item_disabled_press,"WidgetsListItem_OnSetOnPressDisableSlot");
    RESOLVE(list_count,"WidgetsVerticalList_OnSetNoOfItems");
    RESOLVE(list_refresh,"WidgetsVerticalList_RefreshLayout");
    RESOLVE(list_scroll,"WidgetsVerticalList_OnSetScrollOffset");
    RESOLVE(list_item,"WidgetsVerticalList_GetViewForItem");
    RESOLVE(native_load,"CameraCtrlCameraSettingPage_OnLoadItem");
    RESOLVE(open_settings,"CameraCtrlCameraCtrlPage_OnPressSetting");
    RESOLVE(touch_hit,"CoreRoot__DriveMultiTouchHitting");
    RESOLVE(touch_move,"CoreRoot__DriveMultiTouchMovement");
    RESOLVE(touch_point[0],"CoreQuadView__OnSetPoint1");
    RESOLVE(touch_point[1],"CoreQuadView__OnSetPoint2");
    RESOLVE(touch_point[2],"CoreQuadView__OnSetPoint3");
    RESOLVE(touch_point[3],"CoreQuadView__OnSetPoint4");
#undef RESOLVE
    return 0;
}

static void *find_group(void *group,void *type,int depth,int *budget) {
    if(depth>24 || --*budget<0)return NULL;
    if(ew.cast(group,type))return group;
    void *child=NULL;
    while((child=ew.next_view(group,child,0)) && *budget>0) {
        if(child==panel)continue;
        if(ew.cast(child,ew.group_class)) {
            void *found=find_group(child,type,depth+1,budget);
            if(found)return found;
        } else --*budget;
    }
    return NULL;
}
static void *find(void *type) {int budget=4096;return find_group(root_object,type,0,&budget);}

static void open_slot(void *self,void *sender) {
    (void)sender;
    if(self==settings) {open_pending=1;atomic_fetch_add(&ac_status.entries,1);}
}
static void close_slot(void *self,void *sender) {
    (void)sender;
    if(self==panel) {close_pending=1;atomic_fetch_add(&ac_status.closes,1);}
}
static void hotspot_slot(void *self,void *sender) {
    (void)sender;
    if(self!=panel || !counter_text)return;
    atomic_fetch_add(&ac_status.clicks,1); /* button press handled (harness observes this) */
    if(atomic_load(&ac_status.hotspot_busy))return; /* a request is already in flight */
    /* Toggle against last-known state; the worker thread performs the HTTP call
       and refreshes hotspot_on. 2 (unknown) defaults to start. */
    int on=atomic_load(&ac_status.hotspot_on);
    atomic_store(&ac_status.hotspot_request,on==1?2:1);
    ew.text_string(counter_text,string(on==1?"正在关闭热点…":"正在开启热点…"));
}

static void configure_item(void *item,const char *title,void *self,void (*method)(void *,void *)) {
    /* LayoutConfig: bit 1 enables the main label; bit 4 enables the arrow.
       Bit 0 enables an icon, so 0x11 produced a clickable but unlabeled row. */
    ew.item_options(item,0x12);
    ew.item_state(item,0);
    ew.item_disable(item,0);
    ew.item_icon(item,NULL);
    ew.item_font(item,NULL);
    ew.item_prior_font(item,NULL); /* Use the native language-aware label font. */
    ew.item_heading(item,string(""));
    ew.item_foot(item,string(""));
    ew.item_string(item,string(title));
    ew.item_press(item,(AcSlot){self,method});
    ew.item_disabled_press(item,(AcSlot){0});
}

static void menu_load(void *self,void *sender) {
    if(self!=settings)return;
    int count=int_at(self,SETTINGS_NATIVE_COUNT),index=int_at(self,SETTINGS_INDEX);
    if(index!=count) {
        if(original_load.method)original_load.method(original_load.object,sender);
        return;
    }
    void *item=ew.cast(pointer_at(self,SETTINGS_ITEM),ew.item_class);
    if(!item)return;
    configure_item(item,"Action Control",self,open_slot);
    ew.item_count(item,1);
    ew.item_number(item,0);
    ew.embedded(item,1);
    AcSlot refresh={(char *)self+SETTINGS_LIST,ew.list_refresh};
    memcpy((char *)item+ITEM_REFRESH_SLOT,&refresh,sizeof(refresh));
    AcRect r=bounds_of(item),page=bounds_of(self);
    r.x2=r.x1+page.x2-page.x1-32;
    ew.bounds(item,r);
    atomic_fetch_add(&ac_status.menu_loads,1);
}

static void hide_panel(void) {
    if(!panel)return;
    void *parent=pointer_at(panel,VIEW_PARENT);
    if(parent)ew.remove(parent,panel);
    ew.unlock(panel);
    panel=NULL;counter_text=NULL;
    atomic_store(&ac_status.panel_visible,0);
}

static void *make_text(void *parent,AcRect rect,const char *text,uint32_t color) {
    void *view=ew.new_object(ew.text_class,NULL);
    ew.bounds(view,rect);
    ew.text_string(view,string(text));
    void *font=ew.font_resource;
    for(const unsigned char *p=(const unsigned char *)text;*p;++p)
        if(*p>=0x80){font=ew.font_zh_resource;break;}
    ew.text_font(view,ew.load_resource(font,ew.font_class));
    ew.text_color(view,color);
    ew.text_alignment(view,0x12); /* Horizontally and vertically centered. */
    ew.add(parent,view,0);
    return view;
}

static void make_button(AcRect rect,const char *text,void (*slot)(void *,void *)) {
    void *item=ew.new_object(ew.item_class,NULL);
    configure_item(item,text,panel,slot);
    ew.item_count(item,1);ew.item_number(item,0);
    ew.bounds(item,rect);
    ew.add(panel,item,0);
}

static void show_panel(void) {
    if(panel)return;
    void *parent=settings?settings:find(ew.liveview_class);
    if(!parent){atomic_store(&ac_status.error,12);return;}
    AcRect r=bounds_of(parent);
    int width=r.x2-r.x1,height=r.y2-r.y1;
    if(width<240 || height<240 || width>2048 || height>2048) {atomic_store(&ac_status.error,12);return;}
    panel=ew.new_object(ew.group_class,NULL);
    ew.lock(panel);
    ew.bounds(panel,(AcRect){0,0,width,height});
    void *background=ew.new_object(ew.rectangle_class,NULL);
    ew.bounds(background,(AcRect){0,0,width,height});
    ew.rectangle_color(background,0xff161616);
    ew.add(panel,background,0);
    /* Consume uncovered touches so they cannot activate a native row behind us. */
    void *blocker=ew.new_object(ew.touch_class,NULL);
    AcPoint corners[]={{0,0},{width,0},{width,height},{0,height}};
    for(int i=0;i<4;i++)ew.touch_point[i](blocker,corners[i]);
    ew.add(panel,blocker,0);
    make_text(panel,(AcRect){16,16,width-16,66},"Action Control",0xffffffff);
    counter_text=make_text(panel,(AcRect){16,72,width-16,122},"原生菜单已连接",0xffb8b8b8);
    /* Native labeled rows compute their own ~96 px height during layout. */
    make_button((AcRect){16,height/2-24,width-16,height/2+72},"开启/关闭热点",hotspot_slot);
    make_button((AcRect){16,height-112,width-16,height-16},"返回",close_slot);
    ew.add(parent,panel,0);
    atomic_store(&ac_status.panel_visible,1);
}

static void detach_menu(void) {
    if(!settings)return;
    hide_panel();
    AcSlot current;memcpy(&current,(char *)settings+SETTINGS_LOAD_SLOT,sizeof(current));
    if(current.object==settings && current.method==menu_load) {
        memcpy((char *)settings+SETTINGS_LOAD_SLOT,&original_load,sizeof(original_load));
        int count=int_at(settings,SETTINGS_NATIVE_COUNT);
        if(count==29 || count==32)ew.list_count((char *)settings+SETTINGS_LIST,count);
    }
    ew.unlock(settings);settings=NULL;original_load=(AcSlot){0};
    atomic_store(&ac_status.menu_attached,0);
}

static void inspect_menu(void) {
    void *page=find(ew.settings_class);
    atomic_store(&ac_status.control_found,find(ew.control_class)!=NULL);
    if(page!=settings)detach_menu();
    if(!page)return;
    int count=int_at(page,SETTINGS_NATIVE_COUNT);
    /* Init briefly uses six rows before its queued onDebugMode signal runs. */
    if(count!=29 && count!=32)return;
    if(!settings) {
        AcSlot slot;memcpy(&slot,(char *)page+SETTINGS_LOAD_SLOT,sizeof(slot));
        if(slot.object!=page || slot.method!=ew.native_load) {atomic_store(&ac_status.error,6);return;}
        ew.lock(page);settings=page;original_load=slot;
        slot=(AcSlot){page,menu_load};
        memcpy((char *)page+SETTINGS_LOAD_SLOT,&slot,sizeof(slot));
    }
    void *list=(char *)page+SETTINGS_LIST;
    if(int_at(list,LIST_COUNT)!=count+1)ew.list_count(list,count+1);
    atomic_store(&ac_status.native_items,count);
    atomic_store(&ac_status.menu_attached,1);
}

static AcPoint physical_point(AcPoint scene) {
    /* Inverse of OsmoGuiOsmoApp's transform from the 400 x 712 touch surface. */
    switch(int_at(root_object,0x4cd0)) {
        case 0:return scene;
        case 1:return (AcPoint){scene.y,712-scene.x};
        case 2:return (AcPoint){400-scene.x,712-scene.y};
        case 3:return (AcPoint){400-scene.y,scene.x};
        default:return (AcPoint){-1,-1};
    }
}

static void start_gesture(int command) {
    uint64_t from=atomic_load(&ac_status.point_from),to=atomic_load(&ac_status.point_to);
    gesture.from=(AcPoint){(int32_t)from,(int32_t)(from>>32)};
    gesture.to=(AcPoint){(int32_t)to,(int32_t)(to>>32)};
    int width=atomic_load(&ac_status.root_width),height=atomic_load(&ac_status.root_height);
    if(gesture.from.x>=width || gesture.to.x>=width ||
       gesture.from.y>=height || gesture.to.y>=height) {atomic_store(&ac_status.command_result,-4);return;}
    gesture.duration=command==4?80:320;
    gesture.start=ew.ticks();gesture.active=1;
    int hit=ew.touch_hit(root_object,1,0,physical_point(gesture.from));
    if(hit)atomic_fetch_add(&ac_status.touch_hits,1);
    dump_pending=1;
}

void ac_ui_tick(void *context,int32_t *changed) {
    void *outer=pointer_at(context,0);
    if(!outer)return;
    /* TestTestApp delegates rendering/input to its independent Application root. */
    void *application=ew.cast(outer,ew.wrapper_class)?pointer_at(outer,0x4b0):outer;
    void *root=application?ew.cast(application,ew.app_class):NULL;
    if(!root)return;
    if(root_object && root!=root_object) {detach_menu();hide_panel();}
    root_object=root;
    uint32_t now=ew.ticks();
    if(!last_scan || now-last_scan>=50) {
        void *liveview=find(ew.liveview_class);
        AcRect r=bounds_of(settings?settings:(liveview?liveview:root_object));
        atomic_store(&ac_status.root_width,r.x2-r.x1);
        atomic_store(&ac_status.root_height,r.y2-r.y1);
        inspect_menu();last_scan=now;
    }
    if(gesture.active) {
        uint32_t elapsed=now-gesture.start;
        if(elapsed>gesture.duration)elapsed=gesture.duration;
        AcPoint at={gesture.from.x+(gesture.to.x-gesture.from.x)*(int)elapsed/(int)gesture.duration,
                    gesture.from.y+(gesture.to.y-gesture.from.y)*(int)elapsed/(int)gesture.duration};
        ew.touch_move(root_object,0,physical_point(at));
        if(elapsed==gesture.duration) {
            ew.touch_hit(root_object,0,0,physical_point(gesture.to));
            gesture.active=0;dump_pending=1;atomic_store(&ac_status.command_result,1);
        }
        if(changed)*changed=1;
    }
    int command=gesture.active?0:atomic_exchange(&ac_status.command,0);
    if(command) {
        atomic_store(&ac_status.command_result,0);
        if(command==1)open_pending=1;
        else if(command==2)close_pending=1;
        else if(command==3) {
            void *control=find(ew.control_class);
            if(control)ew.open_settings(control,control);
            else atomic_store(&ac_status.command_result,-3);
        } else if(command==4 || command==5)start_gesture(command);
        else if(command==7)dump_pending=1;
        else if(command==6 && settings) {
            void *list=(char *)settings+SETTINGS_LIST;
            void *item=ew.list_item(list,int_at(settings,SETTINGS_NATIVE_COUNT));
            if(item) {
                AcRect row=bounds_of(item),viewport=bounds_of(list);
                int32_t offset=int_at(list,0x6c0);
                ew.list_scroll(list,offset+(viewport.y2-viewport.y1)-16-row.y2);
            } else atomic_store(&ac_status.command_result,-6);
        }
        if(changed)*changed=1;
    }
    if(close_pending) {hide_panel();close_pending=0;if(changed)*changed=1;}
    if(open_pending) {show_panel();open_pending=0;dump_pending=1;if(changed)*changed=1;}
    /* Refresh the hotspot status label only when the worker-observed state
       changes, and never while a toggle is in flight (keeps the "正在…" text). */
    if(panel && counter_text && !atomic_load(&ac_status.hotspot_busy)) {
        int on=atomic_load(&ac_status.hotspot_on);
        if(on!=shown_hotspot) {
            ew.text_string(counter_text,string(on==1?"热点已开启":on==0?"热点已关闭":"热点状态未知"));
            shown_hotspot=on;
            if(changed)*changed=1;
        }
    } else if(!panel) shown_hotspot=-1;
    if(dump_pending)dump_tree();
}
