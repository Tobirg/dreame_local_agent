/* Independent optional firmware interceptor. MIT license. */
#define _GNU_SOURCE
#include <dlfcn.h>
#include <unistd.h>
#include <fcntl.h>
#include <stdio.h>
#include <string.h>
#include <stdlib.h>
#ifndef GUARD_BASE
#define GUARD_BASE "/data/dreame-local"
#endif
#ifndef GUARD_RUNTIME
#define GUARD_RUNTIME "/tmp"
#endif
#ifndef GUARD_NATIVE_LIBRARY
#define GUARD_NATIVE_LIBRARY "/ava/lib/node_msg_cvt.so"
#endif
static const char symbol[]="_ZN13avanodemsgcvt9MsgConfig13SetDeviceLockEi";
static void marker(const char *path,const char *text){
 int fd=open(path,O_WRONLY|O_CREAT|O_TRUNC|O_NOFOLLOW,0600);
 if(fd>=0){(void)write(fd,text,strlen(text));close(fd);}
}
__attribute__((constructor)) static void loaded(void){
 char name[32]={0};int fd=open("/proc/self/comm",O_RDONLY);
 if(fd<0)return;ssize_t n=read(fd,name,sizeof(name)-1);close(fd);
 if(n>0&&strcmp(name,"ava\n")==0){char pid[32];snprintf(pid,sizeof(pid),"%ld\n",(long)getpid());marker(GUARD_RUNTIME "/dreame-region-guard.loaded",pid);}
}
void guard_set_device_lock(void *self,int code) __asm__("_ZN13avanodemsgcvt9MsgConfig13SetDeviceLockEi");
void guard_set_device_lock(void *self,int code){
 if(code!=200&&access(GUARD_BASE "/region-guard.enabled",F_OK)==0){
  /* Do not call the native setter: the blocking command is never applied. */
  marker(GUARD_RUNTIME "/dreame-region-guard.blocked","1\n");return;
 }
 typedef void (*setter)(void *,int);
 setter real=(setter)dlsym(RTLD_NEXT,symbol);
 if(!real){void *handle=dlopen(GUARD_NATIVE_LIBRARY,RTLD_LAZY|RTLD_NOLOAD);if(handle){real=(setter)dlsym(handle,symbol);dlclose(handle);}}
 if(real&&real!=guard_set_device_lock)real(self,code);
}
