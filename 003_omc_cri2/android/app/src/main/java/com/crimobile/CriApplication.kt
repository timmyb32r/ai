package com.crimobile

import android.app.Application
import android.content.Intent

/**
 * Bootstraps the PlayerService so the player is ready before
 * the ViewModel needs it.
 */
class CriApplication : Application() {
    override fun onCreate() {
        super.onCreate()
        startService(Intent(this, PlayerService::class.java))
    }
}
