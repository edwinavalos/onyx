// Guest-side vsock probe (compile with `cc -o vsockprobe vsockprobe.c` in the macOS guest).
//   vsockprobe dial   -> connect to host CID 2 port 5000, send a line, print reply
//   vsockprobe listen -> listen on port 5001, echo one line back
#include <stdio.h>
#include <string.h>
#include <unistd.h>
#include <errno.h>
#include <sys/socket.h>
#include <sys/vsock.h>

int main(int argc, char **argv) {
    int fd = socket(AF_VSOCK, SOCK_STREAM, 0);
    if (fd < 0) { perror("socket(AF_VSOCK)"); return 1; }
    struct sockaddr_vm sa; memset(&sa, 0, sizeof sa);
    sa.svm_family = AF_VSOCK; sa.svm_len = sizeof sa;
    char buf[256];
    if (argc > 1 && strcmp(argv[1], "listen") == 0) {
        sa.svm_cid = VMADDR_CID_ANY; sa.svm_port = 5001;
        if (bind(fd, (struct sockaddr *)&sa, sizeof sa) < 0) { perror("bind"); return 1; }
        if (listen(fd, 1) < 0) { perror("listen"); return 1; }
        printf("listening on vsock port 5001\n"); fflush(stdout);
        for (;;) {
            int c = accept(fd, NULL, NULL);
            if (c < 0) { perror("accept"); return 1; }
            ssize_t n = read(c, buf, sizeof buf - 1);
            if (n > 0) { buf[n] = 0; printf("host said: %s", buf); fflush(stdout); }
            write(c, "hello from guest\n", 17);
            close(c);
        }
    }
    sa.svm_cid = VMADDR_CID_HOST; sa.svm_port = 5000;
    if (connect(fd, (struct sockaddr *)&sa, sizeof sa) < 0) { perror("connect"); return 1; }
    write(fd, "hello from guest\n", 17);
    ssize_t n = read(fd, buf, sizeof buf - 1);
    if (n > 0) { buf[n] = 0; printf("host said: %s", buf); }
    close(fd);
    return 0;
}
