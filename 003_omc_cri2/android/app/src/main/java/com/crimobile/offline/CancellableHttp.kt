package com.crimobile.offline

import kotlinx.coroutines.suspendCancellableCoroutine
import okhttp3.Call
import okhttp3.Callback
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import java.io.IOException
import java.util.concurrent.TimeUnit
import kotlin.coroutines.resume
import kotlin.coroutines.resumeWithException

/** Cancellation and the total deadline remain attached until the body is fully consumed. */
internal suspend fun <T> OkHttpClient.readCancellable(request: Request, read: (Response) -> T): T =
    suspendCancellableCoroutine { continuation ->
        val call = newCall(request)
        val maximum = TimeUnit.SECONDS.toNanos(45)
        if (call.timeout().timeoutNanos() == 0L || call.timeout().timeoutNanos() > maximum) {
            call.timeout().timeout(maximum, TimeUnit.NANOSECONDS)
        }
        continuation.invokeOnCancellation { call.cancel() }
        call.enqueue(object : Callback {
            override fun onFailure(call: Call, e: IOException) {
                if (continuation.isActive) continuation.resumeWithException(e)
            }

            override fun onResponse(call: Call, response: Response) {
                val result = runCatching { response.use(read) }
                if (continuation.isActive) result.fold(continuation::resume, continuation::resumeWithException)
            }
        })
    }
