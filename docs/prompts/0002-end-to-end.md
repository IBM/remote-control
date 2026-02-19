What we have so far is a great start, but there are still a number of things not working. Some of these may be core design problems while others may simply be implementation bugs. Here's a list of what I'm seeing:

**Basic Wrapping Problems**

* When the user launches the host (eg `./remote-control --server http://localhost:8443 bash`), the resulting output is not the fully rich TTY-enabled output I would expect (for `bash`, I don't see the user's PS1 prompt, no color when running `ls`)
* When the host process enters `stdin` input (eg a shell command when wrapping `bash`), the resulting output does not format the same way as if the command were run unwrapped

**Client Connection Problems**

* When a client attempts to connect and the host requires approval, there is a significant lag (likely poll interval too slow)
* Once the host presents the acceptance view, if they select `a` and hit `Enter`, nothing happens and the host is then unable to enter any other input to STIDN
* Input from the client has no effect
* Once host is stuck like this, signal handling also does not work

From here, we need to establish a clean way to test this multi-process setup end-to-end and get all these issues fixed!