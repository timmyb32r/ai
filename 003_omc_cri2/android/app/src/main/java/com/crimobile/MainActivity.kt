package com.crimobile

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.viewModels
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import com.crimobile.ui.CriApp
import com.crimobile.viewmodel.CriViewModel

class MainActivity : ComponentActivity() {

    private val viewModel: CriViewModel by viewModels()

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContent {
            val state by viewModel.state.collectAsState()
            CriApp(
                state = state,
                onAction = viewModel::dispatch
            )
        }
    }
}
